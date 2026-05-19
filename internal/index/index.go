// Package index implements the IVF (Inverted File Index) for vector search.
package index

import (
	"errors"
	"math"

	"rinha-backend/internal/vector"
)

// IVFIndex is an Inverted File Index for approximate nearest neighbor search.
// Vectors are grouped into clusters by their nearest centroid during loading.
// During search, only the vectors in the nearest cluster(s) are compared.
type IVFIndex struct {
	Vectors   []vector.Vector14
	Labels    []uint8  // 0=legit, 1=fraud
	Centroids []vector.Vector14
	Offsets   []int   // offsets[c] = start, offsets[c+1] = end
	nClusters int
}

// NewIVFIndex creates an IVF index from pre-processed data.
func NewIVFIndex(vectors []vector.Vector14, labels []uint8, centroids []vector.Vector14, offsets []int) *IVFIndex {
	return &IVFIndex{
		Vectors:   vectors,
		Labels:    labels,
		Centroids: centroids,
		Offsets:   offsets,
		nClusters: len(centroids),
	}
}

// Search finds the 5 nearest neighbors of query in the IVF index.
//
// Optimization (early exit, ADR-37): searches only the nearest cluster
// (~3000 vectors) if it contains at least 5 vectors. Falls back to a
// second cluster scan only when the first has fewer than 5. This halves
// the per-request work in the common case (~84μs → ~42μs), reducing
// CPU contention and improving throughput under load.
//
// The recall impact is minimal: with 1000 clusters and 3M vectors,
// clusters average ~3000 vectors — queries near cluster boundaries
// may lose 1-2 borderline neighbors, but the 0.6 threshold absorbs
// these small variations.
func (idx *IVFIndex) Search(query *vector.Vector14) (fraudCount int, err error) {
	if idx.nClusters == 0 {
		return 0, errors.New("empty index")
	}

	// 1. Find the nearest centroid
	bestC := 0
	bestD := int32(math.MaxInt32)
	for c := 0; c < idx.nClusters; c++ {
		d := vector.ManhattanDistance(query, &idx.Centroids[c])
		if d < bestD {
			bestD = d
			bestC = c
		}
	}

	// 2. Collect candidate ranges — start with nearest cluster only
	type clusterRange struct{ start, end int }
	ranges := make([]clusterRange, 0, 2)

	start, end := idx.Offsets[bestC], idx.Offsets[bestC+1]
	firstSize := end - start
	ranges = append(ranges, clusterRange{start, end})

	// Only search second cluster if the first has fewer than 5 vectors
	// (edge case: very small clusters near the end of the offset list)
	if firstSize < 5 {
		secondBestC := -1
		secondBestD := int32(math.MaxInt32)
		for c := 0; c < idx.nClusters; c++ {
			if c == bestC {
				continue
			}
			d := vector.ManhattanDistance(query, &idx.Centroids[c])
			if d < secondBestD {
				secondBestD = d
				secondBestC = c
			}
		}
		if secondBestC >= 0 {
			ranges = append(ranges, clusterRange{
				start: idx.Offsets[secondBestC],
				end:   idx.Offsets[secondBestC+1],
			})
		}
	}

	// 3. Search top-5 nearest neighbors within the candidate ranges
	type neighbor struct {
		dist  int32
		label uint8
	}
	top5 := [5]neighbor{
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
	}

	for _, r := range ranges {
		for i := r.start; i < r.end; i++ {
			dist := vector.ManhattanDistance(query, &idx.Vectors[i])

			// Insert into top5 if better than worst
			if dist < top5[4].dist {
				pos := 4
				for pos > 0 && dist < top5[pos-1].dist {
					top5[pos] = top5[pos-1]
					pos--
				}
				top5[pos] = neighbor{dist: dist, label: idx.Labels[i]}
			}
		}
	}

	// 4. Count frauds (label == 1)
	fraudCount = 0
	for _, n := range top5 {
		if n.label == 1 {
			fraudCount++
		}
	}

	return fraudCount, nil
}