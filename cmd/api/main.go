// API server for fraud detection using IVF vector search.
package main

import (
	"log"
	"net/http"
	"os"

	"rinha-backend/internal/handler"
	"rinha-backend/internal/index"
	"rinha-backend/internal/loader"
)

func main() {
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

	idx := index.NewIVFIndex(reorderedVecs, reorderedLabels, centroids, offsets)
	h := handler.New(idx, norm, mccRisk)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", h.Ready)
	mux.HandleFunc("POST /fraud-score", h.FraudScore)

	log.Printf("API server starting on port %s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}