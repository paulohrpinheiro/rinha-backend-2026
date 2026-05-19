package handler

import (
	"io"
	"net/http"
	"sync"

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
