package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"rinha-backend/internal/index"
	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

// semaphore limits concurrent fraud-score requests to prevent
// goroutine explosion and GC thrashing under high load.
// 32 is appropriate for 0.45 CPU: 32 concurrent × ~0.3ms = ~9.6ms
// of simultaneous CPU work, safely within the CPU budget.
// More conservative than the previous 128, reducing GC pressure
// and Go scheduler contention significantly.
var semaphore = make(chan struct{}, 32)

// payloadPool reuses TransactionPayload structs to reduce one allocation
// per request. (json.Unmarshal still allocates internal strings/slices.)
var payloadPool = sync.Pool{
	New: func() any {
		return new(model.TransactionPayload)
	},
}

// responsePool reuses bytes.Buffer for manual JSON response serialization.
var responsePool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
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

// Ready handles GET /ready — returns 200 when the service is ready.
func (h *FraudHandler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// FraudScore handles POST /fraud-score — processes a transaction and
// returns the fraud decision with minimal allocations.
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

	// 2. Parse JSON directly from request body (skip intermediate buffer copy)
	payload := payloadPool.Get().(*model.TransactionPayload)
	defer payloadPool.Put(payload)

	if err := json.NewDecoder(r.Body).Decode(payload); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid JSON"}`))
		return
	}

	// 3. Normalize to 14-dim vector
	queryVec := vector.Normalize(payload, h.norm, h.mccRisk)

	// 4. Search for 5 nearest neighbors
	fraudCount, err := h.index.Search(&queryVec)
	if err != nil {
		// Silent fallback — no log.Printf (avoids syscall in hot path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"approved":true,"fraud_score":0.0}`))
		return
	}

	// 5. Calculate fraud score and decision
	fraudScore := float64(fraudCount) / 5.0
	approved := fraudScore < 0.6

	// 6. Serialize response manually (no reflection, no json.Encoder)
	buf := responsePool.Get().(*bytes.Buffer)
	buf.Reset()
	defer responsePool.Put(buf)

	buf.WriteString(`{"approved":`)
	if approved {
		buf.WriteString(`true`)
	} else {
		buf.WriteString(`false`)
	}
	buf.WriteString(`,"fraud_score":`)
	buf.WriteString(strconv.FormatFloat(fraudScore, 'f', 1, 64))
	buf.WriteByte('}')

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(buf.Bytes())
}
