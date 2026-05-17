// Package handler implements HTTP handlers for the fraud detection API.
package handler

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"rinha-backend/internal/index"
	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

// semaphore limits concurrent fraud-score requests to prevent
// goroutine explosion and GC thrashing under high load.
// 128 allows higher throughput under 0.475 CPU with Unix sockets
// reducing per-request latency. At ~0.3ms per request, 128 concurrent
// = ~38ms of parallel CPU work, within the CPU budget.
var semaphore = make(chan struct{}, 128)

// responsePool reuses bytes.Buffer and json.Encoder allocations for
// fraud score responses, reducing GC pressure under high load.
var responsePool = sync.Pool{
	New: func() any {
		return &bytes.Buffer{}
	},
}

// FraudHandler holds dependencies for the HTTP handlers.
type FraudHandler struct {
	index   *index.IVFIndex
	norm    *model.Normalization
	mccRisk map[string]float64
}

// New creates a new FraudHandler.
func New(idx *index.IVFIndex, norm *model.Normalization, mccRisk map[string]float64) *FraudHandler {
	return &FraudHandler{
		index:   idx,
		norm:    norm,
		mccRisk: mccRisk,
	}
}

// Ready handles GET /ready — returns  ready — returns 200 when the service is ready.
func (h *FraudHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// FraudScore handles POST /fraud-score — processes a transaction and returns the fraud decision.
func (h *FraudHandler) FraudScore(w http.ResponseWriter, r *http.Request) {
	// Acquire semaphore slot; return 503 immediately if at capacity
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	default:
		http.Error(w, `{"error":"too many requests"}`, http.StatusServiceUnavailable)
		return
	}

	// Parse request body
	var payload model.TransactionPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	// Normalize to 14-dim vector
	queryVec := vector.Normalize(&payload, h.norm, h.mccRisk)

	// Search for 5 nearest neighbors
	fraudCount, err := h.index.Search(&queryVec)
	if err != nil {
		log.Printf("Search error: %v", err)
		// Fallback: respond with safe values instead of HTTP error
		resp := model.FraudScoreResponse{
			Approved:   true,
			FraudScore: 0.0,
		}
		w.Header().Set("Content-Type", "application/json")
		buf := responsePool.Get().(*bytes.Buffer)
		buf.Reset()
		json.NewEncoder(buf).Encode(resp)
		w.Write(buf.Bytes())
		responsePool.Put(buf)
		return
	}

	// Calculate fraud score and decision
	fraudScore := float64(fraudCount) / 5.0
	approved := fraudScore < 0.6

	resp := model.FraudScoreResponse{
		Approved:   approved,
		FraudScore: fraudScore,
	}

	w.Header().Set("Content-Type", "application/json")
	// Use pooled buffer to reduce allocations
	buf := responsePool.Get().(*bytes.Buffer)
	buf.Reset()
	defer responsePool.Put(buf)

	if err := json.NewEncoder(buf).Encode(resp); err != nil {
		log.Printf("JSON encode error: %v", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Write(buf.Bytes())
}