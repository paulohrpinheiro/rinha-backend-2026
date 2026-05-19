package handler

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
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

	norm := &vector.NormalizationConfig{
		MaxAmount:            10000,
		MaxInstallments:      12,
		AmountVsAvgRatio:     10,
		MaxMinutes:           1440,
		MaxKm:                1000,
		MaxTxCount24h:        20,
		MaxMerchantAvgAmount: 10000,
		MCCRisk: map[string]float64{
			"5411": 0.15,
			"7802": 0.75,
		},
	}

	return New(idx, norm)
}

func TestReady(t *testing.T) {
	h := newTestHandler()

	req := httptest.NewRequest("GET", "/ready", nil)
	rec := httptest.NewRecorder()

	h.Ready(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if body != `{"status":"ok"}` {
		t.Errorf("Expected status 'ok', got '%s'", body)
	}
}

func TestFraudScore(t *testing.T) {
	h := newTestHandler()

	// Build binary request payload
	payload := &codec.Payload{
		Amount:         100.0,
		Installments:   1,
		RequestedAt:    time.Date(2026, 3, 11, 18, 45, 53, 0, time.UTC),
		AvgAmount:      200.0,
		TxCount24h:     5,
		KnownMerchants: []string{"MERC-001"},
		MerchantID:     "MERC-001",
		MCC:            "5411",
		MerchantAvgAmount: 150.0,
		IsOnline:       false,
		CardPresent:    true,
		KmFromHome:     10.0,
		HasLastTransaction: false,
	}

	var buf bytes.Buffer
	if err := codec.EncodePayload(&buf, payload); err != nil {
		t.Fatalf("Failed to encode payload: %v", err)
	}

	req := httptest.NewRequest("POST", "/fraud-score", &buf)
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Decode binary response (Content-Type: application/octet-stream)
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Expected Content-Type application/octet-stream, got %s", ct)
	}

	resp, err := codec.DecodeResponse(rec.Body)
	if err != nil {
		t.Fatalf("Failed to decode binary response: %v", err)
	}

	if resp.FraudScore < 0 || resp.FraudScore > 1.0 {
		t.Errorf("FraudScore out of range [0,1]: %f", resp.FraudScore)
	}
}

func TestFraudScoreInvalidPayload(t *testing.T) {
	h := newTestHandler()

	// Send garbage bytes (not valid binary payload)
	req := httptest.NewRequest("POST", "/fraud-score", bytes.NewReader([]byte{0xFF, 0x00}))
	req.Header.Set("Content-Type", "application/octet-stream")
	rec := httptest.NewRecorder()

	h.FraudScore(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", rec.Code)
	}
}
