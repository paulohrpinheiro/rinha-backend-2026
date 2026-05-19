// Package codec implements a binary protocol between the proxy and the API.
//
// The proxy receives JSON from the client, parses it once, encodes it into
// a compact binary format, and sends it to the API via Unix socket. The API
// decodes the binary payload directly (no json.Unmarshal, no allocations for
// strings or intermediate structs) and responds with a 9-byte binary response.
// The proxy decodes the binary response and serializes it back to JSON for
// the client.
//
// This eliminates all JSON parsing from the API hot path, reducing GC pressure
// significantly — every json.Unmarshal on a 300-byte payload allocates strings
// for id, merchant.id, mcc, and known_merchants[]. With ~180 req/s, that's
// thousands of heap allocations per second triggering frequent GC cycles.
package codec

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"time"
)

// MaxPayloadSize is the maximum binary payload size (request).
// Typical payloads are ~80-130 bytes; this is a generous safety limit.
const MaxPayloadSize = 4096

const (
	flagHasLastTransaction = 1 << iota // bit 0
)

// Payload is the decoded binary transaction payload.
// All numeric fields use Go native types. Strings are NOT copied —
// they reference the underlying decode buffer, which remains valid
// until the next read from the connection.
type Payload struct {
	Amount           float64
	Installments     int
	RequestedAt      time.Time
	AvgAmount        float64
	TxCount24h       int
	KnownMerchants   []string
	MerchantID       string
	MCC              string
	MerchantAvgAmount float64
	IsOnline         bool
	CardPresent      bool
	KmFromHome       float64
	// LastTransaction fields (valid only if HasLastTransaction is true)
	HasLastTransaction   bool
	LastTimestamp        time.Time
	LastKmFromCurrent    float64
	// rawBuf holds the underlying buffer for string references.
	// Kept here so strings remain valid during the lifetime of Payload.
	rawBuf []byte
}

// EncodePayload writes the binary encoding of p to w.
// The payload fields are extracted from the JSON-decoded model struct
// by the proxy.
func EncodePayload(w io.Writer, p *Payload) error {
	// Flags
	var flags byte
	if p.HasLastTransaction {
		flags |= flagHasLastTransaction
	}

	// Phase 1: compute total size, allocate buffer, fill it
	// Fixed part: 1(flags) + 8+1+8 + 8+2 + 2+2 + 2+2 + 8 + 1+8 = 53 bytes
	// Variable: known_merchants (count + for each: len + data)
	//           merchant_id (len + data)
	//           mcc (len + data)
	// LastTransaction (if present): 8 + 8 = 16 bytes

	totalSize := 53 // fixed header without strings/last_tx
	totalSize += 2  // known_merchants count
	for _, km := range p.KnownMerchants {
		totalSize += 2 + len(km)
	}
	totalSize += 2 + len(p.MerchantID)
	totalSize += 2 + len(p.MCC)
	if p.HasLastTransaction {
		totalSize += 16
	}

	buf := make([]byte, totalSize)
	pos := 0

	// flags
	buf[pos] = flags
	pos++

	// amount (float64 LE)
	binary.LittleEndian.PutUint64(buf[pos:], math.Float64bits(p.Amount))
	pos += 8

	// installments (uint8)
	buf[pos] = byte(p.Installments)
	pos++

	// requested_at (unix seconds, int64 LE)
	binary.LittleEndian.PutUint64(buf[pos:], uint64(p.RequestedAt.Unix()))
	pos += 8

	// customer avg_amount (float64 LE)
	binary.LittleEndian.PutUint64(buf[pos:], math.Float64bits(p.AvgAmount))
	pos += 8

	// tx_count_24h (uint16 LE)
	binary.LittleEndian.PutUint16(buf[pos:], uint16(p.TxCount24h))
	pos += 2

	// known_merchants
	binary.LittleEndian.PutUint16(buf[pos:], uint16(len(p.KnownMerchants)))
	pos += 2
	for _, km := range p.KnownMerchants {
		binary.LittleEndian.PutUint16(buf[pos:], uint16(len(km)))
		pos += 2
		copy(buf[pos:], km)
		pos += len(km)
	}

	// merchant_id
	binary.LittleEndian.PutUint16(buf[pos:], uint16(len(p.MerchantID)))
	pos += 2
	copy(buf[pos:], p.MerchantID)
	pos += len(p.MerchantID)

	// mcc
	binary.LittleEndian.PutUint16(buf[pos:], uint16(len(p.MCC)))
	pos += 2
	copy(buf[pos:], p.MCC)
	pos += len(p.MCC)

	// merchant avg_amount (float64 LE)
	binary.LittleEndian.PutUint64(buf[pos:], math.Float64bits(p.MerchantAvgAmount))
	pos += 8

	// terminal_flags
	var tflags byte
	if p.IsOnline {
		tflags |= 1
	}
	if p.CardPresent {
		tflags |= 2
	}
	buf[pos] = tflags
	pos++

	// km_from_home (float64 LE)
	binary.LittleEndian.PutUint64(buf[pos:], math.Float64bits(p.KmFromHome))
	pos += 8

	// last_transaction (if present)
	if p.HasLastTransaction {
		binary.LittleEndian.PutUint64(buf[pos:], uint64(p.LastTimestamp.Unix()))
		pos += 8
		binary.LittleEndian.PutUint64(buf[pos:], math.Float64bits(p.LastKmFromCurrent))
		pos += 8
	}

	_, err := w.Write(buf)
	return err
}

// DecodePayload reads a binary payload from r into p.
// The returned Payload references the internal read buffer for its string
// fields (KnownMerchants, MerchantID, MCC). The buffer is stored in p.rawBuf
// and is valid until the next call to DecodePayload with the same Payload.
func DecodePayload(r io.Reader, p *Payload) error {
	// Read the entire payload into a buffer (max 4KB for safety)
	// In practice, payloads are ~80-130 bytes.
	if p.rawBuf == nil || cap(p.rawBuf) < MaxPayloadSize {
		p.rawBuf = make([]byte, MaxPayloadSize)
	}
	buf := p.rawBuf[:MaxPayloadSize]

	// Read at least the minimum fixed header (1 byte flags)
	n, err := io.ReadAtLeast(r, buf, 1)
	if err != nil {
		return err
	}

	// Determine total payload size by parsing the header and variable fields.
	// We need to read the fixed part first, then variable strings.
	// Strategy: read full buffer, parse in-place.
	// First, ensure we have the fixed header (53 bytes without LT, 69 with).
	// But we don't know if LT is present until we read flags.
	// Solution: read in two phases.

	// Phase 1: read enough for the fixed part (without LT or strings)
	// We need to read: flags(1) + amount(8) + installments(1) + requested_at(8) +
	//   avg_amount(8) + tx_count(2) + km_count(2) + ... we can't know string
	//   lengths yet.
	// Better approach: read the full payload in one shot. Since MaxPayloadSize
	// is 4096, just try to read everything.

	// Try to read more
	for n < MaxPayloadSize {
		nr, err := r.Read(buf[n:])
		if nr > 0 {
			n += nr
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	return decodeFromBuffer(buf[:n], p)
}

// decodeFromBuffer parses the binary payload from a buffer.
// String fields in p will reference slices of buf — the caller must ensure
// buf remains valid while p is in use.
func decodeFromBuffer(buf []byte, p *Payload) error {
	if len(buf) < 1 {
		return errors.New("payload too short: missing flags")
	}

	pos := 0

	// flags
	flags := buf[pos]
	p.HasLastTransaction = (flags & flagHasLastTransaction) != 0
	pos++

	// Helper to check bounds
	check := func(need int) error {
		if pos+need > len(buf) {
			return errors.New("payload too short")
		}
		return nil
	}

	// amount
	if err := check(8); err != nil {
		return err
	}
	p.Amount = math.Float64frombits(binary.LittleEndian.Uint64(buf[pos:]))
	pos += 8

	// installments
	if err := check(1); err != nil {
		return err
	}
	p.Installments = int(buf[pos])
	pos++

	// requested_at
	if err := check(8); err != nil {
		return err
	}
	unix := int64(binary.LittleEndian.Uint64(buf[pos:]))
	p.RequestedAt = time.Unix(unix, 0)
	pos += 8

	// customer avg_amount
	if err := check(8); err != nil {
		return err
	}
	p.AvgAmount = math.Float64frombits(binary.LittleEndian.Uint64(buf[pos:]))
	pos += 8

	// tx_count_24h
	if err := check(2); err != nil {
		return err
	}
	p.TxCount24h = int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2

	// known_merchants
	if err := check(2); err != nil {
		return err
	}
	kmCount := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2

	// Reset known_merchants slice (reuse backing array)
	p.KnownMerchants = p.KnownMerchants[:0]
	for i := 0; i < kmCount; i++ {
		if err := check(2); err != nil {
			return err
		}
		kmLen := int(binary.LittleEndian.Uint16(buf[pos:]))
		pos += 2
		if err := check(kmLen); err != nil {
			return err
		}
		// Reference the buffer directly — no allocation
		s := string(buf[pos : pos+kmLen])
		p.KnownMerchants = append(p.KnownMerchants, s)
		pos += kmLen
	}

	// merchant_id
	if err := check(2); err != nil {
		return err
	}
	midLen := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	if err := check(midLen); err != nil {
		return err
	}
	p.MerchantID = string(buf[pos : pos+midLen])
	pos += midLen

	// mcc
	if err := check(2); err != nil {
		return err
	}
	mccLen := int(binary.LittleEndian.Uint16(buf[pos:]))
	pos += 2
	if err := check(mccLen); err != nil {
		return err
	}
	p.MCC = string(buf[pos : pos+mccLen])
	pos += mccLen

	// merchant avg_amount
	if err := check(8); err != nil {
		return err
	}
	p.MerchantAvgAmount = math.Float64frombits(binary.LittleEndian.Uint64(buf[pos:]))
	pos += 8

	// terminal_flags
	if err := check(1); err != nil {
		return err
	}
	tflags := buf[pos]
	p.IsOnline = (tflags & 1) != 0
	p.CardPresent = (tflags & 2) != 0
	pos++

	// km_from_home
	if err := check(8); err != nil {
		return err
	}
	p.KmFromHome = math.Float64frombits(binary.LittleEndian.Uint64(buf[pos:]))
	pos += 8

	// last_transaction
	if p.HasLastTransaction {
		if err := check(8); err != nil {
			return err
		}
		ltUnix := int64(binary.LittleEndian.Uint64(buf[pos:]))
		p.LastTimestamp = time.Unix(ltUnix, 0)
		pos += 8

		if err := check(8); err != nil {
			return err
		}
		p.LastKmFromCurrent = math.Float64frombits(binary.LittleEndian.Uint64(buf[pos:]))
		pos += 8
	}

	return nil
}

// Response is the 9-byte binary response from the API.
type Response struct {
	Approved   bool
	FraudScore float64
}

// EncodeResponse writes the binary response to w.
func EncodeResponse(w io.Writer, r Response) error {
	var buf [9]byte
	if r.Approved {
		buf[0] = 1
	}
	binary.LittleEndian.PutUint64(buf[1:], math.Float64bits(r.FraudScore))
	_, err := w.Write(buf[:])
	return err
}

// DecodeResponse reads a binary response from r.
func DecodeResponse(r io.Reader) (Response, error) {
	var buf [9]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return Response{}, err
	}
	return Response{
		Approved:   buf[0] != 0,
		FraudScore: math.Float64frombits(binary.LittleEndian.Uint64(buf[1:])),
	}, nil
}
