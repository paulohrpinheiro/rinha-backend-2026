package vector

import (
	"testing"
	"time"

	"rinha-backend/internal/model"
)

var testNorm = &model.Normalization{
	MaxAmount:            10000,
	MaxInstallments:      12,
	AmountVsAvgRatio:     10,
	MaxMinutes:           1440,
	MaxKm:                1000,
	MaxTxCount24h:        20,
	MaxMerchantAvgAmount: 10000,
}

var testMCCRisk = map[string]float64{
	"5411": 0.15,
	"7802": 0.75,
	"5912": 0.20,
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
	payload := &model.TransactionPayload{
		Transaction: model.TransactionData{
			Amount:       41.12,
			Installments: 2,
			RequestedAt:  time.Date(2026, 3, 11, 18, 45, 53, 0, time.UTC),
		},
		Customer: model.CustomerData{
			AvgAmount:      82.24,
			TxCount24h:     3,
			KnownMerchants: []string{"MERC-003", "MERC-016"},
		},
		Merchant: model.MerchantData{
			ID:        "MERC-016",
			MCC:       "5411",
			AvgAmount: 60.25,
		},
		Terminal: model.TerminalData{
			IsOnline:    false,
			CardPresent: true,
			KmFromHome:  29.23,
		},
		LastTransaction: nil,
	}

	result := Normalize(payload, testNorm, testMCCRisk)

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
	payload := &model.TransactionPayload{
		Transaction: model.TransactionData{
			Amount:       9505.97,
			Installments: 10,
			RequestedAt:  time.Date(2026, 3, 14, 5, 15, 12, 0, time.UTC),
		},
		Customer: model.CustomerData{
			AvgAmount:      81.28,
			TxCount24h:     20,
			KnownMerchants: []string{"MERC-008", "MERC-007", "MERC-005"},
		},
		Merchant: model.MerchantData{
			ID:        "MERC-068",
			MCC:       "7802",
			AvgAmount: 54.86,
		},
		Terminal: model.TerminalData{
			IsOnline:    false,
			CardPresent: true,
			KmFromHome:  952.27,
		},
		LastTransaction: nil,
	}

	result := Normalize(payload, testNorm, testMCCRisk)

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
	if result[5] != -1 {
		t.Errorf("dim5 (no last tx) = %d, want -1", result[5])
	}
	if result[6] != -1 {
		t.Errorf("dim6 (no last tx) = %d, want -1", result[6])
	}
}

func TestNormalizeWithLastTransaction(t *testing.T) {
	now := time.Date(2026, 3, 11, 20, 23, 35, 0, time.UTC)
	last := time.Date(2026, 3, 11, 14, 58, 35, 0, time.UTC)

	payload := &model.TransactionPayload{
		Transaction: model.TransactionData{
			Amount:       41.12,
			Installments: 2,
			RequestedAt:  now,
		},
		Customer: model.CustomerData{
			AvgAmount:      82.24,
			TxCount24h:     3,
			KnownMerchants: []string{"MERC-003", "MERC-016"},
		},
		Merchant: model.MerchantData{
			ID:        "MERC-016",
			MCC:       "5411",
			AvgAmount: 60.25,
		},
		Terminal: model.TerminalData{
			IsOnline:    false,
			CardPresent: true,
			KmFromHome:  29.23,
		},
		LastTransaction: &model.LastTransactionData{
			Timestamp:     last,
			KmFromCurrent: 18.86,
		},
	}

	result := Normalize(payload, testNorm, testMCCRisk)

	if result[5] == -1 {
		t.Errorf("dim5 should not be -1 when last_transaction is present, got %d", result[5])
	}
	if result[6] == -1 {
		t.Errorf("dim6 should not be -1 when last_transaction is present, got %d", result[6])
	}

	// dim5: minutes diff = 20:23:35 - 14:58:35 = 325 min, normalized = 325/1440 = 0.2257
	// quantized = round(0.2257 * 127) = round(28.66) = 29
	minutes := payload.Transaction.RequestedAt.Sub(payload.LastTransaction.Timestamp).Minutes()
	expectedDim5 := Quantize(clamp(minutes / testNorm.MaxMinutes))
	if !withinTolerance(result[5], expectedDim5) {
		t.Errorf("dim5 = %d, expected around %d (minutes=%.1f)", result[5], expectedDim5, minutes)
	}

	// dim6: 18.86 / 1000 = 0.01886, quantized = round(0.01886*127) = round(2.395) = 2
	expectedDim6 := Quantize(clamp(payload.LastTransaction.KmFromCurrent / testNorm.MaxKm))
	if !withinTolerance(result[6], expectedDim6) {
		t.Errorf("dim6 = %d, expected around %d", result[6], expectedDim6)
	}

	// dims 0-4 should match legit case
	legitFloats := []float64{0.0041, 0.1667, 0.05}
	for i := 0; i <= 2; i++ {
		expected := Quantize(legitFloats[i])
		if !withinTolerance(result[i], expected) {
			t.Errorf("dim[%d] = %d, expected around %d", i, result[i], expected)
		}
	}
}

func TestNormalizeEdgeCases(t *testing.T) {
	payload := &model.TransactionPayload{
		Transaction: model.TransactionData{
			Amount:       0,
			Installments: 0,
			RequestedAt:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		Customer: model.CustomerData{
			AvgAmount:      1.0,
			TxCount24h:     0,
			KnownMerchants: []string{},
		},
		Merchant: model.MerchantData{
			ID:        "MERC-999",
			MCC:       "9999",
			AvgAmount: 0,
		},
		Terminal: model.TerminalData{
			IsOnline:    false,
			CardPresent: false,
			KmFromHome:  0,
		},
		LastTransaction: nil,
	}

	result := Normalize(payload, testNorm, testMCCRisk)

	if result[0] != 0 {
		t.Errorf("dim0 (zero amount) = %d, want 0", result[0])
	}
	if result[1] != 0 {
		t.Errorf("dim1 (zero installments) = %d, want 0", result[1])
	}
	if result[2] != 0 {
		t.Errorf("dim2 (zero ratio) = %d, want 0", result[2])
	}
	if result[3] != 0 {
		t.Errorf("dim3 (midnight) = %d, want 0", result[3])
	}
	if result[9] != 0 {
		t.Errorf("dim9 (offline) = %d, want 0", result[9])
	}
	if result[10] != 0 {
		t.Errorf("dim10 (no card) = %d, want 0", result[10])
	}
	if result[11] != 127 {
		t.Errorf("dim11 (unknown merchant) = %d, want 127", result[11])
	}
	// unknown MCC defaults to risk 0.5 => Quantize(0.5) = 64
	if result[12] != 64 {
		t.Errorf("dim12 (default mcc risk) = %d, want 64", result[12])
	}
	if result[13] != 0 {
		t.Errorf("dim13 (zero merchant avg) = %d, want 0", result[13])
	}
}

func BenchmarkManhattanDistance(b *testing.B) {
	v1 := &Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	v2 := &Vector14{127, 64, 32, 16, 8, 4, 2, 1, 0, 127, 64, 32, 16, 8}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ManhattanDistance(v1, v2)
	}
}

func BenchmarkNormalize(b *testing.B) {
	payload := &model.TransactionPayload{
		Transaction: model.TransactionData{
			Amount:       9505.97,
			Installments: 10,
			RequestedAt:  time.Date(2026, 3, 14, 5, 15, 12, 0, time.UTC),
		},
		Customer: model.CustomerData{
			AvgAmount:      81.28,
			TxCount24h:     20,
			KnownMerchants: []string{"MERC-008", "MERC-007", "MERC-005"},
		},
		Merchant: model.MerchantData{
			ID:        "MERC-068",
			MCC:       "7802",
			AvgAmount: 54.86,
		},
		Terminal: model.TerminalData{
			IsOnline:    false,
			CardPresent: true,
			KmFromHome:  952.27,
		},
		LastTransaction: nil,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Normalize(payload, testNorm, testMCCRisk)
	}
}
