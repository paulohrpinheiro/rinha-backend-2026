package index

import (
	"testing"

	"rinha-backend/internal/vector"
)

func TestIVFSearch(t *testing.T) {
	nClusters := 5
	vectorsPerCluster := 20
	n := nClusters * vectorsPerCluster

	vectors := make([]vector.Vector14, n)
	labels := make([]uint8, n)
	centroids := make([]vector.Vector14, nClusters)
	offsets := make([]int, nClusters+1)

	// Create centroids at increasing distances
	for c := 0; c < nClusters; c++ {
		for d := 0; d < 14; d++ {
			centroids[c][d] = int8(c * 10)
		}
		offsets[c] = c * vectorsPerCluster
	}
	offsets[nClusters] = n

	// Create vectors matching their centroids, alternating labels
	fraudIdx := 0
	for c := 0; c < nClusters; c++ {
		for i := 0; i < vectorsPerCluster; i++ {
			idx := c*vectorsPerCluster + i
			vectors[idx] = centroids[c]
			labels[idx] = uint8(fraudIdx % 2)
			fraudIdx++
		}
	}

	idx := NewIVFIndex(vectors, labels, centroids, offsets)

	// Query matching cluster 0 centroid
	query := &centroids[0]
	fraudCount, err := idx.Search(query)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// top-5 from cluster 0: labels alternate 0,1,0,1,0... first 5 are {0,1,0,1,0}
	// fraud count = 2
	if fraudCount != 2 {
		t.Errorf("Expected 2 frauds, got %d", fraudCount)
	}
}

func TestIVFSearchEmpty(t *testing.T) {
	idx := NewIVFIndex(nil, nil, nil, nil)
	query := &vector.Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	_, err := idx.Search(query)
	if err == nil {
		t.Errorf("Expected error for empty index, got nil")
	}
}

func TestIVFSearchExact(t *testing.T) {
	nClusters := 3
	vectorsPerCluster := 10
	n := nClusters * vectorsPerCluster

	vectors := make([]vector.Vector14, n)
	labels := make([]uint8, n)
	centroids := make([]vector.Vector14, nClusters)
	offsets := make([]int, nClusters+1)

	// Cluster 0: all zeros
	for d := 0; d < 14; d++ {
		centroids[0][d] = 0
	}
	// Cluster 1: all 50s
	for d := 0; d < 14; d++ {
		centroids[1][d] = 50
	}
	// Cluster 2: all 100s
	for d := 0; d < 14; d++ {
		centroids[2][d] = 100
	}

	offsets[0] = 0
	offsets[1] = 10
	offsets[2] = 20
	offsets[3] = n

	// Fill cluster 0 with vectors matching centroid 0
	// Labels: first 3 legit, next 7 fraud (top-5 = {0,0,0,1,1} → 2 frauds)
	for i := 0; i < vectorsPerCluster; i++ {
		vectors[i] = centroids[0]
		if i < 3 {
			labels[i] = 0 // legit
		} else {
			labels[i] = 1 // fraud
		}
	}

	// Fill cluster 1 with vectors at distance 50*14=700 from query
	for i := 0; i < vectorsPerCluster; i++ {
		idx := vectorsPerCluster + i
		vectors[idx] = centroids[1]
		labels[idx] = 0
	}

	// Fill cluster 2
	for i := 0; i < vectorsPerCluster; i++ {
		idx := 2*vectorsPerCluster + i
		vectors[idx] = centroids[2]
		labels[idx] = 1
	}

	idx := NewIVFIndex(vectors, labels, centroids, offsets)

	// Query = centroid 0 (all zeros)
	query := &vector.Vector14{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	fraudCount, err := idx.Search(query)
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	// top-5 nearest are vectors from cluster 0 (distance 0)
	// labels[0..4] = {0,0,0,1,1} -> 2 frauds
	expectedFrauds := 2
	if fraudCount != expectedFrauds {
		t.Errorf("Expected %d frauds, got %d", expectedFrauds, fraudCount)
	}

	// Query far from all centroids - should still work
	queryFar := &vector.Vector14{127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127, 127}
	fraudCount, err = idx.Search(queryFar)
	if err != nil {
		t.Fatalf("Search far query failed: %v", err)
	}
	// top-5 nearest from nearest cluster (cluster 2, distance 27*14 = 378)
	// all labels in cluster 2 are 1 -> 5 frauds
	if fraudCount != 5 {
		t.Errorf("Expected 5 frauds for far query, got %d", fraudCount)
	}
}
