package handler

import (
	"io"
	"net/http"
	"sync"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
	"rinha-backend/internal/vector"
)

// semaphore limits concurrent fraud-score requests to prevent
// goroutine explosion and GC thrashing under high load.
// With binary codec (zero JSON allocations), GC pressure is much lower,
// allowing more concurrent requests. Increased from 32 to 64.
var semaphore = make(chan struct{}, 64)

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
	// 1. Acquire semaphore slot; return 503 immediately if at capacity
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"too many requests"}`))
		return
	}

	// 2. Decode binary payload directly (no json.Unmarshal, no allocations)
	payload := payloadPool.Get().(*codec.Payload)
	defer payloadPool.Put(payload)

	if err := codec.DecodePayload(r.Body, payload); err != nil {
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

// discardBody reads and discards any remaining bytes from r.Body.
// Useful when returning early due to error, to allow connection reuse.
func discardBody(r *http.Request) {
	io.Copy(io.Discard, r.Body)
}
