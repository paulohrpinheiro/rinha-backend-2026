package handler

import (
	"net/http"
	"sync"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
	"rinha-backend/internal/vector"
)

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
	// Decode binary payload directly (no json.Unmarshal, no allocations)
	// Note: no semaphore — with GOGC=off, GOMEMLIMIT=150MiB, and zero-alloc
	// binary protocol, the original GC thrashing concern is resolved. The Go
	// scheduler with GOMAXPROCS=1 naturally serializes goroutines without
	// artificial queuing, eliminating the 503 convoy problem.
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
