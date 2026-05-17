// API server for fraud detection using IVF vector search.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"rinha-backend/internal/handler"
	"rinha-backend/internal/index"
	"rinha-backend/internal/loader"
)

func main() {
	// If -build-index flag is set, pre-build the IVF index to a file and exit.
	// This is used during Docker build to produce a ready-to-load index,
	// reducing startup time from ~90s to <1s.
	buildIndexPath := flag.String("build-index", "", "Pre-build IVF index to file and exit")
	flag.Parse()

	if *buildIndexPath != "" {
		buildIndex(*buildIndexPath)
		return
	}

	// File paths (resources are in the same directory as the binary)
	resourcesDir := "resources"
	if dir := os.Getenv("RESOURCES_DIR"); dir != "" {
		resourcesDir = dir
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Loading normalization constants...")
	norm, err := loader.LoadNormalization(resourcesDir + "/normalization.json")
	if err != nil {
		log.Fatalf("Failed to load normalization.json: %v", err)
	}
	log.Println("  OK")

	log.Println("Loading MCC risk map...")
	mccRisk, err := loader.LoadMCCRisk(resourcesDir + "/mcc_risk.json")
	if err != nil {
		log.Fatalf("Failed to load mcc_risk.json: %v", err)
	}
	log.Println("  OK")

	// Try loading pre-built index first (fast path)
	var idx *index.IVFIndex
	indexPath := resourcesDir + "/index.bin"
	if data, err := loader.LoadIndex(indexPath); err == nil {
		log.Printf("Loaded pre-built index from %s (%d vectors)\n", indexPath, len(data.Vectors))
		idx = index.NewIVFIndex(data.Vectors, data.Labels, data.Centroids, data.Offsets)
	} else {
		log.Println("Pre-built index not found, building from references.json.gz...")
		log.Println("Loading and quantizing reference vectors (3M)...")
		vectors, labels, err := loader.LoadReferences(resourcesDir + "/references.json.gz")
		if err != nil {
			log.Fatalf("Failed to load references.json.gz: %v", err)
		}
		log.Printf("  Loaded %d vectors\n", len(vectors))

		log.Println("Building IVF index with 1000 clusters...")
		nClusters := 1000
		reorderedVecs, reorderedLabels, centroids, offsets, err := loader.BuildIVFIndex(vectors, labels, nClusters)
		if err != nil {
			log.Fatalf("Failed to build IVF index: %v", err)
		}
		log.Println("  OK")

		// Free original arrays (help GC)
		vectors = nil
		labels = nil

		idx = index.NewIVFIndex(reorderedVecs, reorderedLabels, centroids, offsets)
	}

	h := handler.New(idx, norm, mccRisk)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", h.Ready)
	mux.HandleFunc("POST /fraud-score", h.FraudScore)

	log.Printf("API server starting on port %s\n", port)
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      2 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4096,
	}
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// buildIndex loads the dataset, builds the IVF index, and saves it to a binary file.
// This is intended to be called during Docker build for fast startup at runtime.
func buildIndex(path string) {
	resourcesDir := "resources"
	if dir := os.Getenv("RESOURCES_DIR"); dir != "" {
		resourcesDir = dir
	}

	log.Println("Loading normalization constants...")
	_, err := loader.LoadNormalization(resourcesDir + "/normalization.json")
	if err != nil {
		log.Fatalf("Failed to load normalization.json: %v", err)
	}
	log.Println("  OK")

	log.Println("Loading MCC risk map...")
	_, err = loader.LoadMCCRisk(resourcesDir + "/mcc_risk.json")
	if err != nil {
		log.Fatalf("Failed to load mcc_risk.json: %v", err)
	}
	log.Println("  OK")

	log.Println("Loading and quantizing reference vectors (3M)...")
	vectors, labels, err := loader.LoadReferences(resourcesDir + "/references.json.gz")
	if err != nil {
		log.Fatalf("Failed to load references.json.gz: %v", err)
	}
	log.Printf("  Loaded %d vectors\n", len(vectors))

	log.Println("Building IVF index with 1000 clusters...")
	nClusters := 1000
	reorderedVecs, reorderedLabels, centroids, offsets, err := loader.BuildIVFIndex(vectors, labels, nClusters)
	if err != nil {
		log.Fatalf("Failed to build IVF index: %v", err)
	}
	log.Println("  OK")

	// Free original arrays (help GC)
	vectors = nil
	labels = nil

	log.Printf("Saving pre-built index to %s...\n", path)
	if err := loader.SaveIndex(path, reorderedVecs, reorderedLabels, centroids, offsets); err != nil {
		log.Fatalf("Failed to save index: %v", err)
	}
	log.Println("  OK")
}
