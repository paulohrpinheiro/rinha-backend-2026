package handler

import (
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
	"rinha-backend/internal/vector"
)

// semaphore limits concurrent fraud-score requests (non-blocking, large
// capacity). 1024 slots rarely fills — at 180 req/s and ~45μs CPU each on
// 0.425 cores, only ~10-20 slots are in use at steady state.
//
// Non-blocking (select/default) returns fast 503 when full instead of
// blocking. The blocking semaphore (128) was worse than no semaphore:
// it caused all 128 slots to fill, API goroutines parked, proxy waited,
// proxy slots filled, k6 connections hung — 99% failure rate.
//
// With 1024 slots and non-blocking behavior, 503s are extremely rare
// and, when they happen, fast (<1μs) — preventing cascade.
var semaphore = make(chan struct{}, 1024)

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

// FraudScore handles POST /fraud-score — processes a transaction and
// returns the fraud decision using binary codec (zero JSON allocations).
func (h *FraudHandler) FraudScore(w http.ResponseWriter, r *http.Request) {
	// Acquire non-blocking semaphore slot. 1024 capacity under normal load
	// (180 req/s) means ~10-20 concurrent — never fills. If full (extreme
	// burst), fast 503 prevents cascade (blocking caused 99% failure rate).
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"too many requests"}`))
		return
	}

	// Decode binary payload directly via DecodeBytes (no io.Reader blocking).
	// Read r.Body once — http.Server's body reader respects Content-Length
	// and returns exactly the proxy's binary payload (~130 bytes), no extra
	// blocking reads. Then parse from the byte slice.
	payload := payloadPool.Get().(*codec.Payload)
	defer payloadPool.Put(payload)

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"cannot read body"}`))
		return
	}
	if err := codec.DecodeBytes(bodyBytes, payload); err != nil {
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
	// The proxy will decode this and serialize to JSON for the client.
	// Use io.MultiWriter? No, we just write directly.
	// We need a header so the proxy knows this is a binary response.
	// Content-Type: application/octet-stream signals binary body.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)

	// Write directly to the response writer — no buffering needed
	if err := codec.EncodeResponse(w, codec.Response{
		Approved:   approved,
		FraudScore: fraudScore,
	}); err != nil {
		// If write fails, connection is broken anyway — nothing to do
		return
	}
}

// Warmup runs a few representative searches right after loading the IVF
// index, before the HTTP server starts accepting requests. This:
//   - Warms CPU caches (L1/L2/L3) for the vector search hot path
//   - Touches cluster data across different centroid regions
//   - Causes Go runtime to compile and inline the hot functions
//   - Absorbs first-access page faults and memory allocation costs
//
// 16 searches across 4 payload varieties cover enough diversity to warm
// multiple clusters and execution paths.
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

	for round := 0; round < 4; round++ {
		for i := range payloads {
			q := vector.Normalize(&payloads[i], h.norm)
			h.index.Search(&q)
		}
	}

	log.Printf("Warmup complete in %s", time.Since(start))
}
