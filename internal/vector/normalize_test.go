package vector

import (
	"testing"
	"time"

	"rinha-backend/internal/codec"
)

var testNorm = &NormalizationConfig{
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
		"5912": 0.20,
	},
}

func withinTolerance(actual, expected int8) bool {
	diff := int(actual) - int(expected)
	if diff < 0 {
		diff = -diff
	}
	return diff <= 1
}

func TestQuantize(t *testing.T) {
	tests := []struct {
		input    float64
		expected int8
	}{
		{0.0, 0},
		{1.0, 127},
		{0.5, 64},
		{-1.0, -1},
		{-0.5, 0},
		{1.5, 127},
		{0.0041, 1},
		{0.1667, 21},
		{0.7826, 99},
		{0.3333, 42},
		{0.0292, 4},
		{0.15, 19},
		{0.006, 1},
		{0.9506, 121},
		{0.8333, 106},
		{0.2174, 28},
		{0.9523, 121},
		{0.0055, 1},
	}
	for _, tt := range tests {
		got := Quantize(tt.input)
		if got != tt.expected {
			t.Errorf("Quantize(%v) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestManhattanDistance(t *testing.T) {
	vEqual1 := &Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	vEqual2 := &Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if d := ManhattanDistance(vEqual1, vEqual2); d != 0 {
		t.Errorf("equal vectors distance = %d, want 0", d)
	}

	vA := &Vector14{10, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	vB := &Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if d := ManhattanDistance(vA, vB); d != 10 {
		t.Errorf("single diff distance = %d, want 10", d)
	}

	vC := &Vector14{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	vD := &Vector14{14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	if d := ManhattanDistance(vC, vD); d != 98 {
		t.Errorf("multi-diff distance = %d, want 98", d)
	}

	vE := &Vector14{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	vF := &Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if d := ManhattanDistance(vE, vF); d != 14 {
		t.Errorf("sentinel distance = %d, want 14", d)
	}
	if d := ManhattanDistance(vE, vE); d != 0 {
		t.Errorf("both sentinel distance = %d, want 0", d)
	}

	vMax := &Vector14{127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127}
	if d := ManhattanDistance(vEqual1, vMax); d != 127*14 {
		t.Errorf("max distance = %d, want %d", d, 127*14)
	}
}

func TestNormalizeLegitTx(t *testing.T) {
	payload := &codec.Payload{
		Amount:         41.12,
		Installments:   2,
		RequestedAt:    time.Date(2026, 3, 11, 18, 45, 53, 0, time.UTC),
		AvgAmount:      82.24,
		TxCount24h:     3,
		KnownMerchants: []string{"MERC-003", "MERC-016"},
		MerchantID:     "MERC-016",
		MCC:            "5411",
		MerchantAvgAmount: 60.25,
		IsOnline:       false,
		CardPresent:    true,
		KmFromHome:     29.23,
		HasLastTransaction: false,
	}

	result := Normalize(payload, testNorm)

	expectedFloats := []float64{
		0.0041, 0.1667, 0.05, 0.7826, 0.5,
		-1, -1,
		0.0292, 0.15, 0, 1, 0, 0.15, 0.006,
	}

	for i, f := range expectedFloats {
		expected := Quantize(f)
		if !withinTolerance(result[i], expected) {
			t.Errorf("legit dim[%d] = %d (float=%v), expected around %d (float=%v), diff > 1",
				i, result[i], f, expected, f)
		}
	}

	if result[9] != 0 {
		t.Errorf("dim9 (is_online) = %d, want 0", result[9])
	}
	if result[10] != 127 {
		t.Errorf("dim10 (card_present) = %d, want 127", result[10])
	}
	if result[11] != 0 {
		t.Errorf("dim11 (known_merchant) = %d, want 0", result[11])
	}
	if result[5] != -1 {
		t.Errorf("dim5 (no last tx) = %d, want -1", result[5])
	}
	if result[6] != -1 {
		t.Errorf("dim6 (no last tx) = %d, want -1", result[6])
	}
}

func TestNormalizeFraudTx(t *testing.T) {
	payload := &codec.Payload{
		Amount:         9505.97,
		Installments:   10,
		RequestedAt:    time.Date(2026, 3, 14, 5, 15, 12, 0, time.UTC),
		AvgAmount:      81.28,
		TxCount24h:     20,
		KnownMerchants: []string{"MERC-008", "MERC-007", "MERC-005"},
		MerchantID:     "MERC-068",
		MCC:            "7802",
		MerchantAvgAmount: 54.86,
		IsOnline:       false,
		CardPresent:    true,
		KmFromHome:     952.27,
		HasLastTransaction: false,
	}

	result := Normalize(payload, testNorm)

	expectedFloats := []float64{
		0.9506, 0.8333, 1.0, 0.2174, 1.0,
		-1, -1,
		0.9523, 1.0, 0, 1, 1, 0.75, 0.0055,
	}

	for i, f := range expectedFloats {
		expected := Quantize(f)
		if !withinTolerance(result[i], expected) {
			t.Errorf("fraud dim[%d] = %d (float=%v), expected around %d (float=%v), diff > 1",
				i, result[i], f, expected, f)
		}
	}

	if result[9] != 0 {
		t.Errorf("dim9 (is_online) = %d, want 0", result[9])
	}
	if result[10] != 127 {
		t.Errorf("dim10 (card_present) = %d, want 127", result[10])
	}
	if result[11] != 127 {
		t.Errorf("dim11 (unknown_merchant) = %d, want 127", result[11])
	}
}

func TestNormalizeLastTx(t *testing.T) {
	payload := &codec.Payload{
		Amount:         150.0,
		Installments:   2,
		RequestedAt:    time.Date(2026, 3, 14, 10, 30, 0, 0, time.UTC),
		AvgAmount:      200.0,
		TxCount24h:     5,
		KnownMerchants: []string{"MERC-001"},
		MerchantID:     "MERC-001",
		MCC:            "5912",
		MerchantAvgAmount: 100.0,
		IsOnline:       true,
		CardPresent:    false,
		KmFromHome:     50.0,
		HasLastTransaction: true,
		LastTimestamp:  time.Date(2026, 3, 14, 10, 25, 0, 0, time.UTC),
		LastKmFromCurrent: 10.0,
	}

	result := Normalize(payload, testNorm)

	if result[5] == -1 {
		t.Errorf("dim5 (minutes since last tx) should not be -1")
	}
	if result[6] == -1 {
		t.Errorf("dim6 (km from last tx) should not be -1")
	}

	// minutes since last tx: 5 min → 5/1440 ≈ 0.00347 → quantize → 0
	if result[5] != 0 {
		t.Errorf("dim5 (5min) = %d, expected 0", result[5])
	}
	// km from last tx: 10 → 10/1000 = 0.01 → quantize → 1
	if result[6] != 1 {
		t.Errorf("dim6 (10km) = %d, expected 1", result[6])
	}
	// is_online
	if result[9] != 127 {
		t.Errorf("dim9 (is_online) = %d, want 127", result[9])
	}
	// card_present false
	if result[10] != 0 {
		t.Errorf("dim10 (card_present) = %d, want 0", result[10])
	}
}

func TestNormalizeMissingMCC(t *testing.T) {
	payload := &codec.Payload{
		Amount:         100.0,
		Installments:   1,
		RequestedAt:    time.Date(2026, 3, 11, 12, 0, 0, 0, time.UTC),
		AvgAmount:      200.0,
		TxCount24h:     1,
		KnownMerchants: []string{"MERC-001"},
		MerchantID:     "MERC-001",
		MCC:            "9999",
		MerchantAvgAmount: 100.0,
		IsOnline:       false,
		CardPresent:    true,
		KmFromHome:     10.0,
		HasLastTransaction: false,
	}

	result := Normalize(payload, testNorm)

	// Missing MCC defaults to 0.5 → quantize(0.5) = 64
	if result[12] != 64 {
		t.Errorf("dim12 (missing mcc risk) = %d, want 64 (risk=0.5)", result[12])
	}
}
