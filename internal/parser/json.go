package parser

import (
	"time"
	"unsafe"
)

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
	HasLastTransaction   bool
	LastTimestamp        time.Time
	LastKmFromCurrent    float64
}

func ParseJSON(body []byte, p *Payload) error {
	*p = Payload{}
	var knownBuf [32]string
	var knownCount int

	pos := skipTo(body, 0, '{')
	if pos < 0 { return errInvalidJSON }
	pos++

	depth := 1
	for pos < len(body) && depth > 0 {
		c := body[pos]
		switch c {
		case '}': depth--; pos++
		case '{': depth++; pos++
		case '"':
			keyStart := pos + 1
			keyEnd := findQuote(body, keyStart)
			if keyEnd < 0 { return errInvalidJSON }
			keyLen := keyEnd - keyStart
			pos = keyEnd + 1
			pos = skipTo(body, pos, ':')
			if pos < 0 { return errInvalidJSON }
			pos++
			pos = skipWhitespace(body, pos)

			switch keyLen {
			case 6: // "amount"
				if body[keyStart] == 'a' { v, n := parseFloat(body, pos); p.Amount = v; pos = n } else { pos = skipValue(body, pos) }
			case 12: // "installments"
				if body[keyStart] == 'i' { v, n := parseInt(body, pos); p.Installments = v; pos = n } else { pos = skipValue(body, pos) }
			case 13: // "requested_at"
				if body[keyStart] == 'r' { ts, n := parseTimestamp(body, pos); p.RequestedAt = ts; pos = n } else { pos = skipValue(body, pos) }
			case 11: // "tx_count_24h" or "merchant_id"
				if body[keyStart] == 't' { v, n := parseInt(body, pos); p.TxCount24h = v; pos = n } else { pos = skipValue(body, pos) }
			case 8: // "customer", "merchant", "terminal"
				switch body[keyStart] {
				case 'c': pos = parseCustomer(body, pos, p, &knownBuf, &knownCount)
				case 'm': pos = parseMerchant(body, pos, p)
				case 't': pos = parseTerminal(body, pos, p)
				default: pos = skipValue(body, pos)
				}
			case 16: // "last_transaction"
				if body[keyStart] == 'l' { pos = parseLastTransaction(body, pos, p) } else { pos = skipValue(body, pos) }
			default:
				pos = skipValue(body, pos)
			}
			pos = skipToCommaOrClose(body, pos)
		case ',', '\n', '\r', ' ', '\t': pos++
		default: pos++
		}
	}

	if knownCount > 0 {
		p.KnownMerchants = make([]string, knownCount)
		copy(p.KnownMerchants, knownBuf[:knownCount])
	}
	return nil
}

func parseCustomer(body []byte, pos int, p *Payload, knownBuf *[32]string, knownCount *int) int {
	pos = skipTo(body, pos, '{') + 1
	depth := 1
	for pos < len(body) && depth > 0 {
		c := body[pos]
		switch c {
		case '}': depth--; pos++
		case '{': depth++; pos++
		case '"':
			keyStart := pos + 1
			keyEnd := findQuote(body, keyStart)
			if keyEnd < 0 { return len(body) }
			keyLen := keyEnd - keyStart
			pos = keyEnd + 1
			pos = skipTo(body, pos, ':') + 1
			pos = skipWhitespace(body, pos)
			switch keyLen {
			case 10: // "avg_amount"
				v, n := parseFloat(body, pos); p.AvgAmount = v; pos = n
			case 11: // "tx_count_24h"
				v, n := parseInt(body, pos); p.TxCount24h = v; pos = n
			case 15: // "known_merchants"
				pos = parseKnownMerchants(body, pos, knownBuf, knownCount)
			default: pos = skipValue(body, pos)
			}
			pos = skipToCommaOrClose(body, pos)
		case ',', '\n', '\r', ' ', '\t': pos++
		default: pos++
		}
	}
	return pos
}

func parseMerchant(body []byte, pos int, p *Payload) int {
	pos = skipTo(body, pos, '{') + 1
	depth := 1
	for pos < len(body) && depth > 0 {
		c := body[pos]
		switch c {
		case '}': depth--; pos++
		case '{': depth++; pos++
		case '"':
			keyStart := pos + 1
			keyEnd := findQuote(body, keyStart)
			if keyEnd < 0 { return len(body) }
			keyLen := keyEnd - keyStart
			pos = keyEnd + 1
			pos = skipTo(body, pos, ':') + 1
			pos = skipWhitespace(body, pos)
			switch keyLen {
			case 2: // "id"
				s, n := parseString(body, pos); p.MerchantID = s; pos = n
			case 3: // "mcc"
				s, n := parseString(body, pos); p.MCC = s; pos = n
			case 10: // "avg_amount"
				v, n := parseFloat(body, pos); p.MerchantAvgAmount = v; pos = n
			default: pos = skipValue(body, pos)
			}
			pos = skipToCommaOrClose(body, pos)
		case ',', '\n', '\r', ' ', '\t': pos++
		default: pos++
		}
	}
	return pos
}

func parseTerminal(body []byte, pos int, p *Payload) int {
	pos = skipTo(body, pos, '{') + 1
	depth := 1
	for pos < len(body) && depth > 0 {
		c := body[pos]
		switch c {
		case '}': depth--; pos++
		case '{': depth++; pos++
		case '"':
			keyStart := pos + 1
			keyEnd := findQuote(body, keyStart)
			if keyEnd < 0 { return len(body) }
			keyLen := keyEnd - keyStart
			pos = keyEnd + 1
			pos = skipTo(body, pos, ':') + 1
			pos = skipWhitespace(body, pos)
			switch keyLen {
			case 9: // "is_online"
				p.IsOnline = body[pos] == 't'; pos = skipValue(body, pos)
			case 12: // "card_present" or "km_from_home"
				if body[keyStart] == 'c' {
					p.CardPresent = body[pos] == 't'
					pos = skipValue(body, pos)
				} else if body[keyStart] == 'k' {
					v, n := parseFloat(body, pos); p.KmFromHome = v; pos = n
				} else { pos = skipValue(body, pos) }
			default: pos = skipValue(body, pos)
			}
			pos = skipToCommaOrClose(body, pos)
		case ',', '\n', '\r', ' ', '\t': pos++
		default: pos++
		}
	}
	return pos
}

func parseLastTransaction(body []byte, pos int, p *Payload) int {
	if pos+3 < len(body) && body[pos] == 'n' && body[pos+1] == 'u' && body[pos+2] == 'l' && body[pos+3] == 'l' {
		return pos + 4
	}
	p.HasLastTransaction = true
	pos = skipTo(body, pos, '{') + 1
	depth := 1
	for pos < len(body) && depth > 0 {
		c := body[pos]
		switch c {
		case '}': depth--; pos++
		case '{': depth++; pos++
		case '"':
			keyStart := pos + 1
			keyEnd := findQuote(body, keyStart)
			if keyEnd < 0 { return len(body) }
			keyLen := keyEnd - keyStart
			pos = keyEnd + 1
			pos = skipTo(body, pos, ':') + 1
			pos = skipWhitespace(body, pos)
			switch keyLen {
			case 9: // "timestamp"
				ts, n := parseTimestamp(body, pos); p.LastTimestamp = ts; pos = n
			case 15: // "km_from_current"
				v, n := parseFloat(body, pos); p.LastKmFromCurrent = v; pos = n
			default: pos = skipValue(body, pos)
			}
			pos = skipToCommaOrClose(body, pos)
		case ',', '\n', '\r', ' ', '\t': pos++
		default: pos++
		}
	}
	return pos
}

var errInvalidJSON = &parseError{"invalid JSON"}
type parseError struct{ msg string }
func (e *parseError) Error() string { return e.msg }

func skipWhitespace(body []byte, pos int) int {
	for pos < len(body) && (body[pos] == ' ' || body[pos] == '\t' || body[pos] == '\n' || body[pos] == '\r') { pos++ }
	return pos
}
func skipTo(body []byte, pos int, target byte) int {
	for i := pos; i < len(body); i++ { if body[i] == target { return i } }
	return -1
}
func findQuote(body []byte, start int) int {
	for i := start; i < len(body); i++ { if body[i] == '"' && body[i-1] != '\\' { return i } }
	return -1
}
func skipToCommaOrClose(body []byte, pos int) int {
	for pos < len(body) {
		switch body[pos] {
		case ',', '}': return pos
		case '"': end := findQuote(body, pos+1); if end < 0 { return len(body) }; pos = end + 1; continue
		case '{': d := 1; pos++; for pos < len(body) && d > 0 { if body[pos] == '{' { d++ }; if body[pos] == '}' { d-- }; pos++ }; continue
		case '[': d := 1; pos++; for pos < len(body) && d > 0 { if body[pos] == '[' { d++ }; if body[pos] == ']' { d-- }; pos++ }; continue
		default: pos++
		}
	}
	return pos
}
func skipValue(body []byte, pos int) int {
	if pos >= len(body) { return pos }
	switch body[pos] {
	case '"': return findQuote(body, pos+1) + 1
	case 't', 'f': for pos < len(body) && body[pos] >= 'a' && body[pos] <= 'z' { pos++ }; return pos
	case 'n': return pos + 4
	case '{': d := 1; pos++; for pos < len(body) && d > 0 { if body[pos] == '{' { d++ }; if body[pos] == '}' { d-- }; pos++ }; return pos
	case '[': d := 1; pos++; for pos < len(body) && d > 0 { if body[pos] == '[' { d++ }; if body[pos] == ']' { d-- }; pos++ }; return pos
	default: for pos < len(body) && ((body[pos] >= '0' && body[pos] <= '9') || body[pos] == '.' || body[pos] == '-' || body[pos] == 'e' || body[pos] == 'E') { pos++ }; return pos
	}
}
func parseFloat(body []byte, pos int) (float64, int) {
	neg := false
	if pos < len(body) && body[pos] == '-' { neg = true; pos++ }
	var ival int64
	for pos < len(body) && body[pos] >= '0' && body[pos] <= '9' { ival = ival*10 + int64(body[pos]-'0'); pos++ }
	var frac float64
	if pos < len(body) && body[pos] == '.' {
		pos++; div := 10.0
		for pos < len(body) && body[pos] >= '0' && body[pos] <= '9' { frac += float64(body[pos]-'0') / div; div *= 10; pos++ }
	}
	r := float64(ival) + frac
	if neg { r = -r }
	return r, pos
}
func parseInt(body []byte, pos int) (int, int) {
	var v int
	for pos < len(body) && body[pos] >= '0' && body[pos] <= '9' { v = v*10 + int(body[pos]-'0'); pos++ }
	return v, pos
}
func parseString(body []byte, pos int) (string, int) {
	if pos >= len(body) || body[pos] != '"' { return "", pos }
	start := pos + 1
	end := findQuote(body, start)
	if end < 0 { return "", pos }
	return unsafe.String(&body[start], end-start), end + 1
}
func parseTimestamp(body []byte, pos int) (time.Time, int) {
	if pos >= len(body) || body[pos] != '"' { return time.Time{}, pos }
	start := pos + 1
	end := start + 20
	if end < len(body) && body[end] == '"' {
		year := 1000*int(body[start]-'0') + 100*int(body[start+1]-'0') + 10*int(body[start+2]-'0') + int(body[start+3]-'0')
		month := time.Month(10*int(body[start+5]-'0') + int(body[start+6]-'0'))
		day := 10*int(body[start+8]-'0') + int(body[start+9]-'0')
		hour := 10*int(body[start+11]-'0') + int(body[start+12]-'0')
		min := 10*int(body[start+14]-'0') + int(body[start+15]-'0')
		sec := 10*int(body[start+17]-'0') + int(body[start+18]-'0')
		return time.Date(year, month, day, hour, min, sec, 0, time.UTC), end + 1
	}
	ts, _ := time.Parse(time.RFC3339, string(body[start:end]))
	return ts, end + 1
}
func parseKnownMerchants(body []byte, pos int, buf *[32]string, count *int) int {
	if pos >= len(body) || body[pos] != '[' { return pos }
	pos++; *count = 0
	for pos < len(body) {
		if body[pos] == ']' { return pos + 1 }
		if body[pos] == '"' {
			start := pos + 1
			end := findQuote(body, start)
			if end < 0 { return pos }
			if *count < 32 { buf[*count] = unsafe.String(&body[start], end-start); *count++ }
			pos = end + 1
		} else { pos++ }
	}
	return pos
}
