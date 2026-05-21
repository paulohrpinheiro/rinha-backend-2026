package vector

import (
	"testing"
	"time"

	"rinha-backend/internal/parser"
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

func TestQuantize(t *testing.T) {
	tests := []struct {
		input    float64
		expected int16
	}{
		{0.0, 0},
		{1.0, 10000},
		{0.5, 5000},
		{-1.0, -1},
		{-0.5, 0},
		{1.5, 10000},
		{0.0041, 41},
		{0.1667, 1667},
		{0.7826, 7826},
		{0.3333, 3333},
		{0.0292, 292},
		{0.15, 1500},
		{0.006, 60},
		{0.9506, 9506},
		{0.8333, 8333},
		{0.2174, 2174},
		{0.9523, 9523},
		{0.0055, 55},
	}
	for _, tt := range tests {
		got := Quantize(tt.input)
		if got != tt.expected {
			t.Errorf("Quantize(%v) = %d, want %d", tt.input, got, tt.expected)
		}
	}
}

func TestManhattanDistance(t *testing.T) {
	vEqual1 := &Vector14{}
	vEqual2 := &Vector14{}
	if d := ManhattanDistance(vEqual1, vEqual2); d != 0 {
		t.Errorf("equal vectors distance = %d, want 0", d)
	}

	vA := &Vector14{10}
	vB := &Vector14{}
	if d := ManhattanDistance(vA, vB); d != 10 {
		t.Errorf("single diff distance = %d, want 10", d)
	}

	vC := &Vector14{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	vD := &Vector14{14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	if d := ManhattanDistance(vC, vD); d != 98 {
		t.Errorf("multi-diff distance = %d, want 98", d)
	}

	vE := &Vector14{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	vF := &Vector14{}
	if d := ManhattanDistance(vE, vF); d != 14 {
		t.Errorf("sentinel distance = %d, want 14", d)
	}
	if d := ManhattanDistance(vE, vE); d != 0 {
		t.Errorf("both sentinel distance = %d, want 0", d)
	}

	vMax := &Vector14{10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000, 10000}
	if d := ManhattanDistance(vEqual1, vMax); d != 10000*14 {
		t.Errorf("max distance = %d, want %d", d, 10000*14)
	}
}

func TestEuclideanDistanceSquared(t *testing.T) {
	vEqual1 := &Vector14{}
	vEqual2 := &Vector14{}
	if d := EuclideanDistanceSquared(vEqual1, vEqual2); d != 0 {
		t.Errorf("equal vectors distance = %d, want 0", d)
	}

	vA := &Vector14{10}
	vB := &Vector14{}
	if d := EuclideanDistanceSquared(vA, vB); d != 100 {
		t.Errorf("single diff distance = %d, want 100", d)
	}

	vC := &Vector14{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	vD := &Vector14{14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	// diff each dim: 13,11,9,7,5,3,1,-1,-3,-5,-7,-9,-11,-13
	// squares: 169+121+81+49+25+9+1+1+9+25+49+81+121+169 = 910
	if d := EuclideanDistanceSquared(vC, vD); d != 910 {
		t.Errorf("multi-diff dist = %d, want 910", d)
	}

	vE := &Vector14{-1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1, -1}
	vF := &Vector14{}
	if d := EuclideanDistanceSquared(vE, vF); d != 14 {
		t.Errorf("sentinel dist = %d, want 14", d)
	}
	if d := EuclideanDistanceSquared(vE, vE); d != 0 {
		t.Errorf("both sentinel dist = %d, want 0", d)
	}
}

func TestNormalizeLegitTx(t *testing.T) {
	payload := &parser.Payload{
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

	// Check key dims
	if result[5] != -1 {
		t.Errorf("dim5 (no last tx) = %d, want -1", result[5])
	}
	if result[6] != -1 {
		t.Errorf("dim6 (no last tx) = %d, want -1", result[6])
	}
	if result[9] != 0 {
		t.Errorf("dim9 (is_online) = %d, want 0", result[9])
	}
	if result[10] != 10000 {
		t.Errorf("dim10 (card_present) = %d, want 10000", result[10])
	}
	if result[11] != 0 {
		t.Errorf("dim11 (known_merchant) = %d, want 0 (MERC-016 is known)", result[11])
	}

	// dim0: 41.12 / 10000 = 0.004112 * 10000 = 41
	if result[0] != 41 {
		t.Errorf("dim0 (amount) = %d, want 41", result[0])
	}
}

func TestNormalizeFraudTx(t *testing.T) {
	payload := &parser.Payload{
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

	if result[9] != 0 {
		t.Errorf("dim9 (is_online) = %d, want 0", result[9])
	}
	if result[10] != 10000 {
		t.Errorf("dim10 (card_present) = %d, want 10000", result[10])
	}
	if result[11] != 10000 {
		t.Errorf("dim11 (unknown_merchant) = %d, want 10000 (MERC-068 not in known)", result[11])
	}
	// dim12: mcc_risk 7802 = 0.75 * 10000 = 7500
	if result[12] != 7500 {
		t.Errorf("dim12 (mcc_risk 7802) = %d, want 7500", result[12])
	}
	// dim0: 9505.97 / 10000 = 0.950597 * 10000 = 9506
	if result[0] != 9506 {
		t.Errorf("dim0 (amount) = %d, want 9506", result[0])
	}
}

func TestNormalizeLastTx(t *testing.T) {
	payload := &parser.Payload{
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

	// minutes since last tx: 5 min → 5/1440 ≈ 0.00347 → quantize → 35
	if result[5] != 35 {
		t.Errorf("dim5 (5min) = %d, expected 35", result[5])
	}
	// km from last tx: 10 → 10/1000 = 0.01 → quantize → 100
	if result[6] != 100 {
		t.Errorf("dim6 (10km) = %d, expected 100", result[6])
	}
	// is_online
	if result[9] != 10000 {
		t.Errorf("dim9 (is_online) = %d, want 10000", result[9])
	}
	// card_present false
	if result[10] != 0 {
		t.Errorf("dim10 (card_present) = %d, want 0", result[10])
	}
}

func TestNormalizeMissingMCC(t *testing.T) {
	payload := &parser.Payload{
		Amount:         100.0,
		Installments:   1,
		RequestedAt:    time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC),
		AvgAmount:      200.0,
		TxCount24h:     5,
		KnownMerchants: []string{},
		MerchantID:     "MERC-999",
		MCC:            "9999",
		MerchantAvgAmount: 50.0,
		IsOnline:       false,
		CardPresent:    false,
		KmFromHome:     10.0,
		HasLastTransaction: false,
	}

	result := Normalize(payload, testNorm)

	// dim12: mcc not in lookup → default 0.5 → quantize → 5000
	if result[12] != 5000 {
		t.Errorf("dim12 (missing mcc) = %d, want 5000 (default 0.5)", result[12])
	}
}
