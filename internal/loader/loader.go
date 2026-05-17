// Package loader handles loading and preprocessing of reference datasets.
package loader

import (
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"os"
	"unsafe"

	"rinha-backend/internal/model"
	"rinha-backend/internal/vector"
)

// Binary format for the pre-built IVF index:
//
//	Magic      [4]byte   "IVF\x01" (magic + version)
//	NumVectors  uint32   little-endian
//	NumCentroids uint32  little-endian
//	Vectors     []byte   numVectors * 14 bytes (int8)
//	Labels      []byte   numVectors bytes (uint8)
//	Centroids   []byte   numCentroids * 14 bytes (int8)
//	Offsets     []byte   (numCentroids + 1) * 4 bytes (int32 LE)
const indexMagic = "IVF\x01"

// IndexData holds a deserialized pre-built index.
type IndexData struct {
	Vectors   []vector.Vector14
	Labels    []uint8
	Centroids []vector.Vector14
	Offsets   []int
}

// SaveIndex serializes the IVF index components to a binary file.
func SaveIndex(path string, vectors []vector.Vector14, labels []uint8, centroids []vector.Vector14, offsets []int) error {
	nVectors := uint32(len(vectors))
	nCentroids := uint32(len(centroids))

	if nVectors == 0 || nCentroids == 0 {
		return errors.New("empty vectors or centroids")
	}
	if len(labels) != int(nVectors) {
		return errors.New("labels length mismatch")
	}
	if len(offsets) != int(nCentroids)+1 {
		return errors.New("offsets length mismatch")
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	// 1. Write magic header
	if _, err := f.Write([]byte(indexMagic)); err != nil {
		return err
	}

	// 2. Write metadata
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], nVectors)
	binary.LittleEndian.PutUint32(buf[4:8], nCentroids)
	if _, err := f.Write(buf[:]); err != nil {
		return err
	}

	// 3. Write vectors (nVectors * 14 bytes)
	vecLen := int(nVectors) * 14
	vecBytes := make([]byte, vecLen)
	for i, v := range vectors {
		off := i * 14
		for d := 0; d < 14; d++ {
			vecBytes[off+d] = byte(v[d])
		}
	}
	if _, err := f.Write(vecBytes); err != nil {
		return err
	}

	// 4. Write labels (nVectors bytes)
	if _, err := f.Write(labels); err != nil {
		return err
	}

	// 5. Write centroids (nCentroids * 14 bytes)
	centLen := int(nCentroids) * 14
	centBytes := make([]byte, centLen)
	for i, c := range centroids {
		off := i * 14
		for d := 0; d < 14; d++ {
			centBytes[off+d] = byte(c[d])
		}
	}
	if _, err := f.Write(centBytes); err != nil {
		return err
	}

	// 6. Write offsets ((nCentroids+1) * 4 bytes as int32)
	offLen := (int(nCentroids) + 1) * 4
	offBytes := make([]byte, offLen)
	for i, o := range offsets {
		binary.LittleEndian.PutUint32(offBytes[i*4:(i+1)*4], uint32(o))
	}
	if _, err := f.Write(offBytes); err != nil {
		return err
	}

	return nil
}

// LoadIndex deserializes a pre-built IVF index from a binary file.
func LoadIndex(path string) (*IndexData, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if len(data) < 12 {
		return nil, errors.New("index file too small")
	}

	if string(data[:4]) != indexMagic {
		return nil, errors.New("invalid index magic or version")
	}

	nVectors := binary.LittleEndian.Uint32(data[4:8])
	nCentroids := binary.LittleEndian.Uint32(data[8:12])

	if nVectors == 0 || nCentroids == 0 {
		return nil, errors.New("empty index data")
	}

	vecOffset := 12
	vecLen := int(nVectors) * 14
	labelsOffset := vecOffset + vecLen
	labelsLen := int(nVectors)
	centOffset := labelsOffset + labelsLen
	centLen := int(nCentroids) * 14
	offOffset := centOffset + centLen
	offLen := (int(nCentroids) + 1) * 4

	if len(data) < offOffset+offLen {
		return nil, errors.New("index file truncated")
	}

	// Read vectors — convert byte slice to int8 vectors
	vecSlice := data[vecOffset : vecOffset+vecLen]
	vectors := make([]vector.Vector14, nVectors)
	for i := range vectors {
		off := i * 14
		// Use unsafe to cast byte slice to int8 array pointer, then copy
		src := unsafe.Slice((*int8)(unsafe.Pointer(&vecSlice[off])), 14)
		copy(vectors[i][:], src)
	}

	// Read labels
	labels := make([]uint8, labelsLen)
	copy(labels, data[labelsOffset:labelsOffset+labelsLen])

	// Read centroids
	centSlice := data[centOffset : centOffset+centLen]
	centroids := make([]vector.Vector14, nCentroids)
	for i := range centroids {
		off := i * 14
		src := unsafe.Slice((*int8)(unsafe.Pointer(&centSlice[off])), 14)
		copy(centroids[i][:], src)
	}

	// Read offsets
	offSlice := data[offOffset : offOffset+offLen]
	offsets := make([]int, nCentroids+1)
	for i := range offsets {
		offsets[i] = int(binary.LittleEndian.Uint32(offSlice[i*4 : (i+1)*4]))
	}

	return &IndexData{
		Vectors:   vectors,
		Labels:    labels,
		Centroids: centroids,
		Offsets:   offsets,
	}, nil
}

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

// LoadReferences reads and quantizes the full reference dataset from a gzip file
// using streaming JSON to avoid loading all float64 vectors into memory at once.
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

	dec := json.NewDecoder(gr)

	// Read opening '['
	t, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if t != json.Delim('[') {
		return nil, nil, nil // empty
	}

	// Pre-allocate for 3M vectors (will grow if needed)
	vectors := make([]vector.Vector14, 0, 3_000_000)
	labels := make([]uint8, 0, 3_000_000)

	// Process each reference individually using streaming
	for dec.More() {
		var ref Reference
		if err := dec.Decode(&ref); err != nil {
			return nil, nil, err
		}

		var v vector.Vector14
		for d := 0; d < 14; d++ {
			v[d] = vector.Quantize(ref.Vector[d])
		}
		var label uint8
		if ref.Label == "fraud" {
			label = 1
		}
		vectors = append(vectors, v)
		labels = append(labels, label)

		// Allow GC to reclaim the float64 slice immediately
		ref.Vector = nil
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
		// Pick random batch — avoid rng.Perm(n) which allocates n ints (~24MB)
		batch := make([]int, batchSize)
		for i := range batch {
			batch[i] = rng.IntN(n)
		}

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
