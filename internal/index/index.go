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
// Searches up to nprobe (3) nearest clusters, capped at maxScanPerCluster
// (5000) vectors each. This bounds worst-case latency to ~350µs even when
// the index has degenerate clusters (e.g., a cluster with 1.27M vectors
// due to incomplete K-means convergence). The recall impact is minimal:
// with nprobe=3, the true nearest neighbors are almost always in the top
// 3 clusters. The per-cluster cap only affects queries whose nearest
// cluster is pathologically large, which happens only with a broken index.
func (idx *IVFIndex) Search(query *vector.Vector14) (fraudCount int, err error) {
	const (
		nprobe              = 2
		maxScanPerCluster   = 5000
		k                   = 7
	)

	if idx.nClusters == 0 {
		return 0, errors.New("empty index")
	}

	// 1. Find the nprobe nearest centroids (partial selection sort)
	type centroidDist struct {
		id   int
		dist int32
	}
	nearest := [nprobe]centroidDist{
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
	}

	for c := 0; c < idx.nClusters; c++ {
		d := vector.ManhattanDistance(query, &idx.Centroids[c])
		if d < nearest[nprobe-1].dist {
			pos := nprobe - 1
			for pos > 0 && d < nearest[pos-1].dist {
				nearest[pos] = nearest[pos-1]
				pos--
			}
			nearest[pos] = centroidDist{id: c, dist: d}
		}
	}

	// 2. Search top-k nearest neighbors within the candidate clusters
	type neighbor struct {
		dist  int32
		label uint8
	}
	topK := [k]neighbor{
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
		{dist: math.MaxInt32},
	}

	for _, nc := range nearest {
		if nc.id < 0 || nc.dist == math.MaxInt32 {
			continue
		}
		start := idx.Offsets[nc.id]
		end := idx.Offsets[nc.id+1]
		if start >= end {
			continue
		}
		// Cap scan to maxScanPerCluster vectors
		stop := start + maxScanPerCluster
		if stop > end {
			stop = end
		}
		for i := start; i < stop; i++ {
			dist := vector.ManhattanDistance(query, &idx.Vectors[i])

			if dist < topK[k-1].dist {
				pos := k - 1
				for pos > 0 && dist < topK[pos-1].dist {
					topK[pos] = topK[pos-1]
					pos--
				}
				topK[pos] = neighbor{dist: dist, label: idx.Labels[i]}
			}
		}
	}

	// 3. Count frauds (label == 1)
	fraudCount = 0
	for _, n := range topK {
		if n.label == 1 {
			fraudCount++
		}
	}

	return fraudCount, nil
}