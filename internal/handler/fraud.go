package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
	"rinha-backend/internal/vector"
)

// semaphore limits concurrent fraud-score requests (non-blocking, 256 slots).
// 256 slots provides headroom for bursts while keeping scheduler pressure low
// with GOMAXPROCS=1. At steady state (~25 concurrent, 130μs each), max queue
// depth is ~33ms.
var semaphore = make(chan struct{}, 256)

// APICounters tracks internal metrics for diagnosing where requests are lost.
type APICounters struct {
	RequestsReceived atomic.Int64 // total POST /fraud-score received
	ResponsesSent    atomic.Int64 // 200 OK with binary response
	DecodeErrors     atomic.Int64 // codec.DecodeBytes failed
	ReadErrors       atomic.Int64 // io.ReadAll body read failed
	SearchErrors     atomic.Int64 // h.index.Search returned error (silent fallback)
	Semaphore503s    atomic.Int64 // semaphore full → 503
}

var apiCounters APICounters

// payloadPool reuses codec.Payload structs and their internal buffers
// (rawBuf, KnownMerchants slice) across requests, avoiding allocations.
var payloadPool = sync.Pool{
	New: func() any {
		return new(codec.Payload)
	},
}

// FraudHandler holds dependencies for the HTTP handlers.
type FraudHandler struct {
	index *index.IVFIndex
	norm  *vector.NormalizationConfig
}

// New creates a new FraudHandler.
func New(idx *index.IVFIndex, norm *vector.NormalizationConfig) *FraudHandler {
	return &FraudHandler{
		index: idx,
		norm:  norm,
	}
}

// Ready handles GET /ready — returns 200 when the service is ready.
func (h *FraudHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// DebugVars handles GET /debug/vars — exposes internal counters for diagnostics.
func (h *FraudHandler) DebugVars(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"api":{`+
		`"requests_received":%d,`+
		`"responses_sent":%d,`+
		`"decode_errors":%d,`+
		`"read_errors":%d,`+
		`"search_errors":%d,`+
		`"semaphore_503s":%d`+
		`}}`+"\n",
		apiCounters.RequestsReceived.Load(),
		apiCounters.ResponsesSent.Load(),
		apiCounters.DecodeErrors.Load(),
		apiCounters.ReadErrors.Load(),
		apiCounters.SearchErrors.Load(),
		apiCounters.Semaphore503s.Load(),
	)
}

// FraudScore handles POST /fraud-score — processes a transaction and
// returns the fraud decision using binary codec (zero JSON allocations).
func (h *FraudHandler) FraudScore(w http.ResponseWriter, r *http.Request) {
	apiCounters.RequestsReceived.Add(1)

	// Acquire non-blocking semaphore slot.
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	default:
		apiCounters.Semaphore503s.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"too many requests"}`))
		return
	}

	// Decode binary payload directly via DecodeBytes (no io.Reader blocking).
	payload := payloadPool.Get().(*codec.Payload)
	defer payloadPool.Put(payload)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		apiCounters.ReadErrors.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"cannot read body"}`))
		return
	}
	if err := codec.DecodeBytes(bodyBytes, payload); err != nil {
		apiCounters.DecodeErrors.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid payload"}`))
		return
	}

	// 3. Normalize to 14-dim vector
	queryVec := vector.Normalize(payload, h.norm)

	// 4. Search for 5 nearest neighbors
	fraudCount, err := h.index.Search(&queryVec)
	if err != nil {
		apiCounters.SearchErrors.Add(1)
		// Silent fallback
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"approved":true,"fraud_score":0.0}`))
		return
	}

	// 5. Calculate fraud score and decision
	fraudScore := float64(fraudCount) / 5.0
	approved := fraudScore < 0.6

	// 6. Write binary response (9 bytes: approved bool + fraud_score float64)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	if err := codec.EncodeResponse(w, codec.Response{
		Approved:   approved,
		FraudScore: fraudScore,
	}); err != nil {
		return
	}

	apiCounters.ResponsesSent.Add(1)
}

// Warmup runs representative searches after loading the IVF index, before
// the HTTP server starts accepting requests. This:
//   - Warms CPU caches (L1/L2/L3) for the vector search hot path
//   - Touches cluster data across diverse centroid regions
//   - Causes Go runtime to compile and inline the hot functions
//   - Absorbs first-access page faults and memory allocation costs
//
// 64 searches across 4 payload varieties × 16 rounds cover enough diversity
// to warm most of the 1000 clusters' data paths.
func (h *FraudHandler) Warmup() {
	log.Print("Warming up IVF index...")
	start := time.Now()

	payloads := []codec.Payload{
		// in_person, card present, with last_transaction
		{Amount: 384.88, Installments: 3, RequestedAt: time.Now(),
			AvgAmount: 769.76, TxCount24h: 3,
			KnownMerchants: []string{"MERC-001", "MERC-009"},
			MerchantID: "MERC-001", MCC: "5912", MerchantAvgAmount: 298.95,
			IsOnline: false, CardPresent: true, KmFromHome: 13.7,
			HasLastTransaction: true, LastTimestamp: time.Now().Add(-5 * time.Hour), LastKmFromCurrent: 18.8},
		// online, card not present, with last_transaction
		{Amount: 2911.41, Installments: 12, RequestedAt: time.Now(),
			AvgAmount: 411.03, TxCount24h: 8,
			KnownMerchants: []string{"MERC-221", "MERC-010"},
			MerchantID: "MERC-551", MCC: "6011", MerchantAvgAmount: 712.22,
			IsOnline: true, CardPresent: false, KmFromHome: 2.18,
			HasLastTransaction: true, LastTimestamp: time.Now().Add(-3 * time.Hour), LastKmFromCurrent: 1.34},
		// in_person, card present, no last_transaction
		{Amount: 41.12, Installments: 2, RequestedAt: time.Now(),
			AvgAmount: 82.24, TxCount24h: 3,
			KnownMerchants: []string{"MERC-003", "MERC-016"},
			MerchantID: "MERC-016", MCC: "5411", MerchantAvgAmount: 60.25,
			IsOnline: false, CardPresent: true, KmFromHome: 29.2,
			HasLastTransaction: false},
		// online, no card, large km, with last_transaction
		{Amount: 87.91, Installments: 1, RequestedAt: time.Now(),
			AvgAmount: 703.28, TxCount24h: 5,
			KnownMerchants: []string{"MERC-003"},
			MerchantID: "MERC-512", MCC: "5814", MerchantAvgAmount: 480.5,
			IsOnline: true, CardPresent: false, KmFromHome: 799.5,
			HasLastTransaction: true, LastTimestamp: time.Now().Add(-8 * time.Hour), LastKmFromCurrent: 793.8},
	}

	for round := 0; round < 16; round++ {
		for i := range payloads {
			q := vector.Normalize(&payloads[i], h.norm)
			h.index.Search(&q)
		}
	}

	log.Printf("Warmup complete in %s", time.Since(start))
}
