// Package vector implements the 14-dimensional vector normalization
// and int8 quantization for the fraud detection system.
package vector

import (
	"math"

	"rinha-backend/internal/codec"
)

// Vector14 is a 14-dimensional vector quantized to int8.
// Range: -1 (sentinel for missing data) or 0-127 (for normalized [0,1] values).
type Vector14 [14]int8

const sentinel int8 = -1

// Quantize converts a float64 in [0,1] to int8 in [0,127].
// Special case: -1.0 is preserved as the sentinel value.
func Quantize(v float64) int8 {
	if v == -1.0 {
		return sentinel
	}
	if v <= 0.0 {
		return 0
	}
	if v >= 1.0 {
		return 127
	}
	return int8(math.Round(v * 127.0))
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

// ManhattanDistance computes the L1 distance between two int8 vectors.
// This is optimized for inlining and auto-vectorization by the compiler.
func ManhattanDistance(a, b *Vector14) int32 {
	// Manually unrolled for performance
	var sum int32
	d := int32(a[0]) - int32(b[0])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[1]) - int32(b[1])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[2]) - int32(b[2])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[3]) - int32(b[3])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[4]) - int32(b[4])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[5]) - int32(b[5])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[6]) - int32(b[6])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[7]) - int32(b[7])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[8]) - int32(b[8])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[9]) - int32(b[9])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[10]) - int32(b[10])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[11]) - int32(b[11])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[12]) - int32(b[12])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
	d = int32(a[13]) - int32(b[13])
	if d >= 0 {
		sum += d
	} else {
		sum -= d
	}
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

// Normalize converts a binary-decoded transaction payload into a quantized
// 14-dimensional vector. It accepts *codec.Payload directly (zero allocations
// for JSON strings).
func Normalize(payload *codec.Payload, norm *NormalizationConfig) Vector14 {
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

	// dim9: is_online (0 or 1 → 0 or 127)
	if payload.IsOnline {
		v[9] = 127
	} else {
		v[9] = 0
	}

	// dim10: card_present (0 or 1)
	if payload.CardPresent {
		v[10] = 127
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
		v[11] = 127
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
