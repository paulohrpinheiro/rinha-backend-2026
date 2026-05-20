// API server for fraud detection using IVF vector search.
// Receives binary-encoded payloads from the proxy via Unix socket.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"rinha-backend/internal/handler"
	"rinha-backend/internal/index"
	"rinha-backend/internal/loader"
	"rinha-backend/internal/vector"
)

func main() {
	// Pin GOMAXPROCS to 1 to match the 0.45 CPU container limit.
	runtime.GOMAXPROCS(1)

	// If -build-index flag is set, pre-build the IVF index to a file and exit.
	buildIndexPath := flag.String("build-index", "", "Pre-build IVF index to file and exit")
	flag.Parse()

	if *buildIndexPath != "" {
		buildIndex(*buildIndexPath)
		return
	}

	// File paths
	resourcesDir := "resources"
	if dir := os.Getenv("RESOURCES_DIR"); dir != "" {
		resourcesDir = dir
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("Loading normalization constants...")
	normModel, err := loader.LoadNormalization(resourcesDir + "/normalization.json")
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

	// Build NormalizationConfig (used directly by vector.Normalize)
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

		vectors = nil
		labels = nil

		idx = index.NewIVFIndex(reorderedVecs, reorderedLabels, centroids, offsets)
	}

	h := handler.New(idx, norm)
	h.Warmup()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /ready", h.Ready)
	mux.HandleFunc("GET /debug/vars", h.DebugVars)
	mux.HandleFunc("POST /fraud-score", h.FraudScore)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 200 * time.Millisecond,
		ReadTimeout:       200 * time.Millisecond,
		WriteTimeout:      200 * time.Millisecond,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4096,
	}

	// Start TCP listener for /ready (external health checks)
	log.Printf("API server starting TCP on port %s\n", port)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("TCP server error: %v", err)
		}
	}()

	// Start Unix socket listener for binary protocol (proxy↔API)
	unixSocketDir := os.Getenv("UNIX_SOCKET_DIR")
	if unixSocketDir == "" {
		unixSocketDir = "/run/sock"
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "api"
	}
	socketPath := filepath.Join(unixSocketDir, hostname+".sock")

	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		log.Printf("Warning: could not remove stale socket %s: %v", socketPath, err)
	}
	if err := os.MkdirAll(unixSocketDir, 0755); err != nil {
		log.Fatalf("Failed to create socket directory %s: %v", unixSocketDir, err)
	}

	unixListener, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatalf("Failed to create Unix socket %s: %v", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0777); err != nil {
		log.Printf("Warning: could not chmod socket %s: %v", socketPath, err)
	}

	log.Printf("API server starting Unix socket on %s\n", socketPath)
	go func() {
		if err := srv.Serve(unixListener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Unix socket server error: %v", err)
		}
	}()

	select {}
}

// buildIndex loads the dataset, builds the IVF index, and saves it to a binary file.
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

	vectors = nil
	labels = nil

	log.Printf("Saving pre-built index to %s...\n", path)
	if err := loader.SaveIndex(path, reorderedVecs, reorderedLabels, centroids, offsets); err != nil {
		log.Fatalf("Failed to save index: %v", err)
	}
	log.Println("  OK")
}
