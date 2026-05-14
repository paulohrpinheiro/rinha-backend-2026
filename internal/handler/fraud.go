// Package handler implements HTTP handlers for the fraud detection API.
package handler

import (
	"encoding/json"
	"log"
	"net/http"

	"rinha-backend/internal/index"
	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

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
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// FraudScore handles POST /fraud-score — processes a transaction and returns the fraud decision.
func (h *FraudHandler) FraudScore(w http.ResponseWriter, r *http.Request) {
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
		json.NewEncoder(w).Encode(resp)
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
	json.NewEncoder(w).Encode(resp)
}