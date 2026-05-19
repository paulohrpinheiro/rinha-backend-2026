// +build ignore

package main

import (
	"fmt"
	"time"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/index"
	"rinha-backend/internal/loader"
	"rinha-backend/internal/vector"
)

func main() {
	// Load index
	idxData, err := loader.LoadIndex("resources/index.bin")
	if err != nil {
		panic(err)
	}
	fmt.Printf("Loaded %d vectors, %d centroids\n", len(idxData.Vectors), len(idxData.Centroids))

	// Cluster sizes
	fmt.Println("\nCluster size distribution (top 10 largest):")
	type clusterInfo struct {
		id   int
		size int
	}
	largest := make([]clusterInfo, 0, 10)
	for c := 0; c < len(idxData.Centroids); c++ {
		start := idxData.Offsets[c]
		end := idxData.Offsets[c+1]
		size := end - start
		if len(largest) < 10 {
			largest = append(largest, clusterInfo{c, size})
		} else {
			// find min
			minIdx := 0
			for i := range largest {
				if largest[i].size < largest[minIdx].size {
					minIdx = i
				}
			}
			if size > largest[minIdx].size {
				largest[minIdx] = clusterInfo{c, size}
			}
		}
	}
	// Sort descending
	for i := 0; i < len(largest); i++ {
		for j := i + 1; j < len(largest); j++ {
			if largest[j].size > largest[i].size {
				largest[i], largest[j] = largest[j], largest[i]
			}
		}
	}
	for _, ci := range largest {
		fmt.Printf("  Cluster %d: %d vectors\n", ci.id, ci.size)
	}

	// Total vectors sanity
	sum := 0
	minSize := 1 << 30
	maxSize := 0
	for c := 0; c < len(idxData.Centroids); c++ {
		size := idxData.Offsets[c+1] - idxData.Offsets[c]
		sum += size
		if size < minSize {
			minSize = size
		}
		if size > maxSize {
			maxSize = size
		}
	}
	fmt.Printf("\nAll clusters: total=%d min=%d max=%d avg=%.1f\n", sum, minSize, maxSize, float64(sum)/float64(len(idxData.Centroids)))

	idx := index.NewIVFIndex(idxData.Vectors, idxData.Labels, idxData.Centroids, idxData.Offsets)

	normModel, err := loader.LoadNormalization("resources/normalization.json")
	if err != nil {
		panic(err)
	}
	mccRisk, err := loader.LoadMCCRisk("resources/mcc_risk.json")
	if err != nil {
		panic(err)
	}
	norm := &vector.NormalizationConfig{
		MaxAmount:            normModel.MaxAmount,
		MaxInstallments:      normModel.MaxInstallments,
		AmountVsAvgRatio:     normModel.AmountVsAvgRatio,
		MaxMinutes:           normModel.MaxMinutes,
		MaxKm:                normModel.MaxKm,
		MaxTxCount24h:        normModel.MaxTxCount24h,
		MaxMerchantAvgAmount: normModel.MaxMerchantAvgAmount,
		MCCRisk:              mccRisk,
	}

	payloads := map[string]codec.Payload{
		"A_card_present": {
			Amount: 384.88, Installments: 3, RequestedAt: time.Date(2026, 3, 11, 20, 23, 35, 0, time.UTC),
			AvgAmount: 769.76, TxCount24h: 3,
			KnownMerchants: []string{"MERC-009", "MERC-001", "MERC-001"},
			MerchantID: "MERC-001", MCC: "5912", MerchantAvgAmount: 298.95,
			IsOnline: false, CardPresent: true, KmFromHome: 13.709,
			HasLastTransaction: true, LastTimestamp: time.Date(2026, 3, 11, 14, 58, 35, 0, time.UTC), LastKmFromCurrent: 18.863,
		},
		"B_no_card": {
			Amount: 2911.41, Installments: 12, RequestedAt: time.Date(2026, 3, 19, 2, 17, 11, 0, time.UTC),
			AvgAmount: 411.03, TxCount24h: 8,
			KnownMerchants: []string{"MERC-221", "MERC-010"},
			MerchantID: "MERC-551", MCC: "6011", MerchantAvgAmount: 712.22,
			IsOnline: true, CardPresent: false, KmFromHome: 2.18,
			HasLastTransaction: true, LastTimestamp: time.Date(2026, 3, 18, 23, 51, 5, 0, time.UTC), LastKmFromCurrent: 1.34,
		},
	}

	for name, p := range payloads {
		fmt.Printf("\n=== %s ===\n", name)
		// Warmup
		for i := 0; i < 100; i++ {
			q := vector.Normalize(&p, norm)
			idx.Search(&q)
		}

		// Benchmark
		const N = 10000
		start := time.Now()
		for i := 0; i < N; i++ {
			q := vector.Normalize(&p, norm)
			idx.Search(&q)
		}
		elapsed := time.Since(start)
		avg := elapsed / N
		fmt.Printf("  Normalize + Search avg: %v (%d ops in %v)\n", avg, N, elapsed)
	}
}
