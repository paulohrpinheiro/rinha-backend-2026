// Package vector implements the 14-dimensional vector normalization
// and int16 quantization for the fraud detection system.
package vector

import (
	"math"

	"rinha-backend/internal/parser"
)

// Vector14 is a 14-dimensional vector quantized to int16.
// Range: -1 (sentinel for missing data) or 0-10000 (for normalized [0,1] values).
type Vector14 [14]int16

const sentinel int16 = -1
const scale = 10000.0

// Quantize converts a float64 in [0,1] to int16 in [0,10000].
// Special case: -1.0 is preserved as the sentinel value.
func Quantize(v float64) int16 {
	if v == -1.0 {
		return sentinel
	}
	if v <= 0.0 {
		return 0
	}
	if v >= 1.0 {
		return int16(scale)
	}
	return int16(math.Round(v * scale))
}

// clamp restricts v to the [0.0, 1.0] range.
func clamp(v float64) float64 {
	if v < 0.0 {
		return 0.0
	}
	if v > 1.0 {
		return 1.0
	}
	return v
}

// EuclideanDistanceSquared computes the squared L2 distance between two int16 vectors.
// This is optimized for inlining and auto-vectorization by the compiler.
func EuclideanDistanceSquared(a, b *Vector14) int32 {
	var sum int32
	d0 := int32(a[0]) - int32(b[0]); sum += d0 * d0
	d1 := int32(a[1]) - int32(b[1]); sum += d1 * d1
	d2 := int32(a[2]) - int32(b[2]); sum += d2 * d2
	d3 := int32(a[3]) - int32(b[3]); sum += d3 * d3
	d4 := int32(a[4]) - int32(b[4]); sum += d4 * d4
	d5 := int32(a[5]) - int32(b[5]); sum += d5 * d5
	d6 := int32(a[6]) - int32(b[6]); sum += d6 * d6
	d7 := int32(a[7]) - int32(b[7]); sum += d7 * d7
	d8 := int32(a[8]) - int32(b[8]); sum += d8 * d8
	d9 := int32(a[9]) - int32(b[9]); sum += d9 * d9
	d10 := int32(a[10]) - int32(b[10]); sum += d10 * d10
	d11 := int32(a[11]) - int32(b[11]); sum += d11 * d11
	d12 := int32(a[12]) - int32(b[12]); sum += d12 * d12
	d13 := int32(a[13]) - int32(b[13]); sum += d13 * d13
	return sum
}

// NormalizationConfig holds the normalization constants and MCC risk map.
type NormalizationConfig struct {
	MaxAmount            float64
	MaxInstallments      float64
	AmountVsAvgRatio     float64
	MaxMinutes           float64
	MaxKm                float64
	MaxTxCount24h        float64
	MaxMerchantAvgAmount float64
	MCCRisk              map[string]float64
}

// Normalize converts a parsed transaction payload into a quantized
// 14-dimensional vector with int16 precision (scale 10000).
func Normalize(payload *parser.Payload, norm *NormalizationConfig) Vector14 {
	var v Vector14

	// dim0: amount (clamped to [0,1], then quantized)
	v[0] = Quantize(clamp(payload.Amount / norm.MaxAmount))

	// dim1: installments
	v[1] = Quantize(clamp(float64(payload.Installments) / norm.MaxInstallments))

	// dim2: amount vs customer avg ratio
	ratio := payload.Amount / payload.AvgAmount
	v[2] = Quantize(clamp(ratio / norm.AmountVsAvgRatio))

	// dim3: hour of day (0-23, UTC)
	hour := payload.RequestedAt.Hour()
	v[3] = Quantize(float64(hour) / 23.0)

	// dim4: day of week (seg=0, dom=6)
	weekday := payload.RequestedAt.Weekday()
	v[4] = Quantize(float64(weekday) / 6.0)

	// dim5: minutes since last tx (-1 if null)
	if !payload.HasLastTransaction {
		v[5] = sentinel
	} else {
		minutes := payload.RequestedAt.Sub(payload.LastTimestamp).Minutes()
		v[5] = Quantize(clamp(minutes / norm.MaxMinutes))
	}

	// dim6: km from last tx (-1 if null)
	if !payload.HasLastTransaction {
		v[6] = sentinel
	} else {
		v[6] = Quantize(clamp(payload.LastKmFromCurrent / norm.MaxKm))
	}

	// dim7: km from home
	v[7] = Quantize(clamp(payload.KmFromHome / norm.MaxKm))

	// dim8: tx count 24h
	v[8] = Quantize(clamp(float64(payload.TxCount24h) / norm.MaxTxCount24h))

	// dim9: is_online (0 or 1 → 0 or 10000)
	if payload.IsOnline {
		v[9] = int16(scale)
	} else {
		v[9] = 0
	}

	// dim10: card_present (0 or 1)
	if payload.CardPresent {
		v[10] = int16(scale)
	} else {
		v[10] = 0
	}

	// dim11: unknown_merchant (1 if merchant not in known_merchants)
	known := false
	for _, km := range payload.KnownMerchants {
		if km == payload.MerchantID {
			known = true
			break
		}
	}
	if !known {
		v[11] = int16(scale)
	} else {
		v[11] = 0
	}

	// dim12: mcc_risk (from lookup table, default 0.5)
	risk, ok := norm.MCCRisk[payload.MCC]
	if !ok {
		risk = 0.5
	}
	v[12] = Quantize(clamp(risk))

	// dim13: merchant avg_amount
	v[13] = Quantize(clamp(payload.MerchantAvgAmount / norm.MaxMerchantAvgAmount))

	return v
}

// ManhattanDistance is kept for backward compatibility with tests and diag.
func ManhattanDistance(a, b *Vector14) int32 {
	var sum int32
	d := int32(a[0]) - int32(b[0]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[1]) - int32(b[1]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[2]) - int32(b[2]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[3]) - int32(b[3]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[4]) - int32(b[4]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[5]) - int32(b[5]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[6]) - int32(b[6]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[7]) - int32(b[7]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[8]) - int32(b[8]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[9]) - int32(b[9]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[10]) - int32(b[10]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[11]) - int32(b[11]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[12]) - int32(b[12]); if d >= 0 { sum += d } else { sum -= d }
	d = int32(a[13]) - int32(b[13]); if d >= 0 { sum += d } else { sum -= d }
	return sum
}
