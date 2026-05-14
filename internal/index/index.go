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
// It searches the nearest cluster (and potentially the second nearest if
// the first cluster has fewer than 5 vectors).
// Returns fraudCount and totalNeighbors (always 5).
func (idx *IVFIndex) Search(query *vector.Vector14) (fraudCount int, err error) {
	if idx.nClusters == 0 {
		return 0, errors.New("empty index")
	}

	// 1. Find top-2 nearest centroids
	type centroidDist struct {
		idx  int
		dist int32
	}
	top2 := [2]centroidDist{
		{idx: 0, dist: math.MaxInt32},
		{idx: 1, dist: math.MaxInt32},
	}

	for c := 0; c < idx.nClusters; c++ {
		d := vector.ManhattanDistance(query, &idx.Centroids[c])
		if d < top2[1].dist {
			if d < top2[0].dist {
				top2[1] = top2[0]
				top2[0] = centroidDist{idx: c, dist: d}
			} else {
				top2[1] = centroidDist{idx: c, dist: d}
			}
		}
	}

	// 2. Collect vectors from top-2 clusters
	//    (always search at least 2 clusters for safety, since each query
	//     might be near the boundary between two clusters)
	totalVectors := 0
	for _, c := range top2 {
		if c.dist != math.MaxInt32 {
			totalVectors += idx.Offsets[c.idx+1] - idx.Offsets[c.idx]
		}
	}

	if totalVectors == 0 {
		return 0, errors.New("no vectors in nearest clusters")
	}

	// 3. Find top-5 nearest neighbors within the candidate set
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

	for _, c := range top2 {
		if c.dist == math.MaxInt32 {
			continue
		}
		start, end := idx.Offsets[c.idx], idx.Offsets[c.idx+1]
		for i := start; i < end; i++ {
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

	// Count frauds (label == 1)
	fraudCount = 0
	for _, n := range top5 {
		if n.label == 1 {
			fraudCount++
		}
	}

	return fraudCount, nil
}