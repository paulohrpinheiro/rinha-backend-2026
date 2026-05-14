// Package loader handles loading and preprocessing of reference datasets.
package loader

import (
	"compress/gzip"
	"encoding/json"
	"math/rand/v2"
	"os"

	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

// Reference is a single entry from references.json.gz.
type Reference struct {
	Vector []float64 `json:"vector"`
	Label  string    `json:"label"`
}

// LoadNormalization reads the normalization constants file.
func LoadNormalization(path string) (*model.Normalization, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var norm model.Normalization
	if err := json.Unmarshal(data, &norm); err != nil {
		return nil, err
	}
	return &norm, nil
}

// LoadMCCRisk reads the MCC risk mapping file.
func LoadMCCRisk(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var mcc map[string]float64
	if err := json.Unmarshal(data, &mcc); err != nil {
		return nil, err
	}
	return mcc, nil
}

// LoadReferences reads and quantizes the full reference dataset from a gzip file.
// Returns the quantized vectors and labels (0=legit, 1=fraud).
func LoadReferences(path string) ([]vector.Vector14, []uint8, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, nil, err
	}
	defer gr.Close()

	var refs []Reference
	dec := json.NewDecoder(gr)
	if err := dec.Decode(&refs); err != nil {
		return nil, nil, err
	}

	vectors := make([]vector.Vector14, len(refs))
	labels := make([]uint8, len(refs))

	for i, ref := range refs {
		for d := 0; d < 14; d++ {
			vectors[i][d] = vector.Quantize(ref.Vector[d])
		}
		if ref.Label == "fraud" {
			labels[i] = 1
		}
	}

	return vectors, labels, nil
}

// BuildIVFIndex builds an IVF index from quantized vectors.
// It selects nClusters centroids at regular intervals and assigns
// each vector to the nearest centroid, then reorders for fast lookups.
func BuildIVFIndex(vectors []vector.Vector14, labels []uint8, nClusters int) ([]vector.Vector14, []uint8, []vector.Vector14, []int, error) {
	n := len(vectors)
	if n == 0 || nClusters <= 0 {
		return nil, nil, nil, nil, nil
	}

	// 1. Initialize centroids by sampling uniformly from the dataset
	centroids := make([]vector.Vector14, nClusters)
	for i := range centroids {
		idx := i * n / nClusters
		if idx >= n {
			idx = n - 1
		}
		centroids[i] = vectors[idx]
	}

	// 2. Run mini-batch K-means (5 iterations on sampled subsets)
	rng := rand.New(rand.NewPCG(42, 0))
	batchSize := n / 20 // 5% sample per iteration
	if batchSize < nClusters {
		batchSize = nClusters * 5
	}
	if batchSize > n {
		batchSize = n
	}

	clusterAssign := make([]int, n) // temp storage

	for iter := 0; iter < 5; iter++ {
		// Pick random batch
		batch := rng.Perm(n)[:batchSize]

		// For each batch vector, assign to nearest centroid
		for _, idx := range batch {
			bestC := 0
			bestD := vector.ManhattanDistance(&vectors[idx], &centroids[0])
			for c := 1; c < nClusters; c++ {
				d := vector.ManhattanDistance(&vectors[idx], &centroids[c])
				if d < bestD {
					bestD = d
					bestC = c
				}
			}
			clusterAssign[idx] = bestC
		}

		// Recompute centroids using float64 averages, then re-quantize
		// For each centroid, accumulate sum and count
		type centroidAccum struct {
			sum   [14]float64
			count int
		}
		accums := make([]centroidAccum, nClusters)
		for _, idx := range batch {
			c := clusterAssign[idx]
			accums[c].count++
			for d := 0; d < 14; d++ {
				accums[c].sum[d] += float64(vectors[idx][d])
			}
		}

		for c := 0; c < nClusters; c++ {
			if accums[c].count > 0 {
				for d := 0; d < 14; d++ {
					avg := accums[c].sum[d] / float64(accums[c].count)
					centroids[c][d] = vector.Quantize(avg / 127.0 * 127.0) // Re-quantize from [0,127] range
				}
			}
		}
	}

	// 3. Assign ALL vectors to nearest centroid
	for i := 0; i < n; i++ {
		bestC := 0
		bestD := vector.ManhattanDistance(&vectors[i], &centroids[0])
		for c := 1; c < nClusters; c++ {
			d := vector.ManhattanDistance(&vectors[i], &centroids[c])
			if d < bestD {
				bestD = d
				bestC = c
			}
		}
		clusterAssign[i] = bestC
	}

	// 4. Count vectors per cluster to compute offsets
	offsets := make([]int, nClusters+1)
	counts := make([]int, nClusters)
	for i := 0; i < n; i++ {
		counts[clusterAssign[i]]++
	}
	total := 0
	for c := 0; c < nClusters; c++ {
		offsets[c] = total
		total += counts[c]
	}
	offsets[nClusters] = total

	// 5. Reorder vectors by cluster (in-place using a temporary array)
	reorderedVecs := make([]vector.Vector14, n)
	reorderedLabels := make([]uint8, n)
	cursor := make([]int, nClusters)
	copy(cursor, offsets)

	for i := 0; i < n; i++ {
		c := clusterAssign[i]
		pos := cursor[c]
		reorderedVecs[pos] = vectors[i]
		reorderedLabels[pos] = labels[i]
		cursor[c]++
	}

	return reorderedVecs, reorderedLabels, centroids, offsets, nil
}