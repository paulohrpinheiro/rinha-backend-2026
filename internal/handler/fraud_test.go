package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rinha-backend/internal/index"
	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

func newTestHandler() *FraudHandler {
	nVectors := 50
	vectors := make([]vector.Vector14, nVectors)
	labels := make([]uint8, nVectors)
	centroids := make([]vector.Vector14, 2)
	offsets := make([]int, 3)

	// centroid 0: all 0
	// centroid 1: all 50
	for d := 0; d < 14; d++ {
		centroids[0][d] = 0
		centroids[1][d] = 50
	}

	offsets[0] = 0
	offsets[1] = 25
	offsets[2] = 50

	// cluster 0: all zeros, labels: first 23 legit (0), last 2 fraud (1)
	for i := 0; i < 25; i++ {
		vectors[i] = centroids[0]
		if i < 23 {
			labels[i] = 0
		} else {
			labels[i] = 1
		}
	}

	// cluster 1: all 50s, labels: all legit
	for i := 25; i < 50; i++ {
		vectors[i] = centroids[1]
		labels[i] = 0
	}

	idx := index.NewIVFIndex(vectors, labels, centroids, offsets)

	norm := &model.Normalization{
		MaxAmount:            10000,
		MaxInstallments:      12,
		AmountVsAvgRatio:     10,
		MaxMinutes:           1440,
		MaxKm:                1000,
		MaxTxCount24h:        20,
		MaxMerchantAvgAmount: 10000,
	}

	mccRisk := map[string]float64{
		"5411": 0.15,
		"7802": 0.75,
	}

	return New(idx, norm, mccRisk)
}

func TestReady(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()

	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("Expected status 'ok', got '%s'", body["status"])
	}
}

func TestFraudScore(t *testing.T) {
	h := newTestHandler()

	payload := map[string]interface{}{
		"id": "test-001",
		"transaction": map[string]interface{}{
			"amount":       100.0,
			"installments": 1,
			"requested_at": "2026-03-11T18:45:53Z",
		},
		"customer": map[string]interface{}{
			"avg_amount":      200.0,
			"tx_count_24h":    5,
			"known_merchants": []string{"MERC-001"},
		},
		"merchant": map[string]interface{}{
			"id":         "MERC-001",
			"mcc":        "5411",
			"avg_amount": 150.0,
		},
		"terminal": map[string]interface{}{
			"is_online":    false,
			"card_present": true,
			"km_from_home": 10.0,
		},
		"last_transaction": nil,
	}

	bodyBytes, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/fraud-score", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}

	var resp model.FraudScoreResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// response must have both fields
	if resp.FraudScore < 0 || resp.FraudScore > 1.0 {
		t.Errorf("FraudScore out of range [0,1]: %f", resp.FraudScore)
	}
}

func TestFraudScoreInvalidJSON(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest("POST", "/fraud-score", strings.NewReader(`{invalid json}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", rec.Code)
	}
}
