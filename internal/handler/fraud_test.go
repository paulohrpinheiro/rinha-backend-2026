package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"rinha-backend/internal/index"
	"rinha-backend/internal/vector"
)

func newTestHandler() *FraudHandler {
	nVectors := 50
	vectors := make([]vector.Vector14, nVectors)
	labels := make([]uint8, nVectors)
	centroids := make([]vector.Vector14, 2)
	offsets := make([]int, 3)

	for d := 0; d < 14; d++ {
		centroids[0][d] = 0
		centroids[1][d] = 50
	}

	offsets[0] = 0
	offsets[1] = 25
	offsets[2] = 50

	for i := 0; i < 25; i++ {
		vectors[i] = centroids[0]
		if i < 23 { labels[i] = 0 } else { labels[i] = 1 }
	}
	for i := 25; i < 50; i++ {
		vectors[i] = centroids[1]
		labels[i] = 0
	}

	idx := index.NewIVFIndex(vectors, labels, centroids, offsets)
	norm := &vector.NormalizationConfig{
		MaxAmount:            10000,
		MaxInstallments:      12,
		AmountVsAvgRatio:     10,
		MaxMinutes:           1440,
		MaxKm:                1000,
		MaxTxCount24h:        20,
		MaxMerchantAvgAmount: 10000,
		MCCRisk: map[string]float64{"5411": 0.15, "7802": 0.75},
	}
	return New(idx, norm)
}

func TestReady(t *testing.T) {
	h := newTestHandler()
	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()
	h.Ready(rec, req)
	if rec.Code != http.StatusOK { t.Errorf("Expected 200, got %d", rec.Code) }
	if rec.Body.String() != `{"status":"ok"}` { t.Errorf("Expected status 'ok', got '%s'", rec.Body.String()) }
}

func TestFraudScore(t *testing.T) {
	h := newTestHandler()

	// JSON payload (no codec — parser.ParseJSON handles it)
	reqBody := `{"id":"t1","transaction":{"amount":100.0,"installments":1,"requested_at":"2026-03-11T18:45:53Z"},"customer":{"avg_amount":200.0,"tx_count_24h":5,"known_merchants":["MERC-001"]},"merchant":{"id":"MERC-001","mcc":"5411","avg_amount":150.0},"terminal":{"is_online":false,"card_present":true,"km_from_home":10.0},"last_transaction":null}`

	req := httptest.NewRequest("POST", "/fraud-score", bytes.NewReader([]byte(reqBody)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify JSON response (no longer binary codec)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Expected Content-Type application/json, got %s", ct)
	}

	body := rec.Body.String()
	if body != `{"approved":true,"fraud_score":0.0}` &&
		body != `{"approved":true,"fraud_score":0.2}` &&
		body != `{"approved":true,"fraud_score":0.4}` &&
		body != `{"approved":false,"fraud_score":0.6}` &&
		body != `{"approved":false,"fraud_score":0.8}` &&
		body != `{"approved":false,"fraud_score":1.0}` {
		t.Errorf("Unexpected fraud response: %s", body)
	}
}

func TestFraudScoreInvalidPayload(t *testing.T) {
	h := newTestHandler()

	// Send empty JSON object (parser extracts no fields, returns zero vector)
	req := httptest.NewRequest("POST", "/fraud-score", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	// Parser is lenient: returns 200 with zero vector (fallback to approved)
	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}
}
