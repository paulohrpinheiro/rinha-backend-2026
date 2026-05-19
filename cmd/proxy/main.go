// Load balancer proxy using binary codec for proxy↔API communication.
// Receives JSON from the client, encodes to binary, forwards to API.
// Receives binary response from API, decodes, serializes to JSON for client.
//
// This eliminates JSON parsing from the API hot path entirely:
// the proxy does json.Unmarshal once (it has 0.10 CPU and handles
// fewer concurrent requests), and the API reads binary directly.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rinha-backend/internal/codec"
	"rinha-backend/internal/model"
)

// fraudResponses holds the 6 possible JSON responses pre-computed.
// Zero serialization in the hot path — just index by fraud count.
var fraudResponses = [6][]byte{
	[]byte(`{"approved":true,"fraud_score":0.0}`),
	[]byte(`{"approved":true,"fraud_score":0.2}`),
	[]byte(`{"approved":true,"fraud_score":0.4}`),
	[]byte(`{"approved":false,"fraud_score":0.6}`),
	[]byte(`{"approved":false,"fraud_score":0.8}`),
	[]byte(`{"approved":false,"fraud_score":1.0}`),
}

// unixSocketDialer returns a dialer that connects to /run/sock/<hostname>.sock.
func unixSocketDialer(socketDir string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{
		Timeout:   2 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		socketPath := socketDir + "/" + host + ".sock"
		return d.DialContext(ctx, "unix", socketPath)
	}
}

// bodyBufPool reuses byte buffers for reading request bodies.
var bodyBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// payloadPool reuses model.TransactionPayload for JSON parsing.
var jsonPayloadPool = sync.Pool{
	New: func() any { return new(model.TransactionPayload) },
}

// codecPayloadPool reuses codec.Payload for binary encoding.
var codecPayloadPool = sync.Pool{
	New: func() any { return new(codec.Payload) },
}

// encodeBufPool reuses bytes.Buffer for binary payload encoding,
// avoiding allocation per request (~130 bytes each).
var encodeBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// proxySem limits concurrent proxy requests (non-blocking, large capacity).
// 1024 slots ensures it rarely fills under normal load (180 req/s uses ~5
// slots). If full under extreme burst, returns fast 503 instead of cascading
// into timeouts — the blocking semaphore (128) caused proxy goroutines to
// park waiting for API responses, filling proxy slots and cascading failures.
var proxySem = make(chan struct{}, 1024)

// RoundRobinProxy handles POST /fraud-score: parses JSON, encodes to binary,
// forwards to an API, decodes binary response, returns JSON.
type RoundRobinProxy struct {
	backends []string
	counter  atomic.Uint64
	client   *http.Client
}

func (p *RoundRobinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Acquire non-blocking semaphore slot. With 1024 capacity, almost never
	// full under normal load (~5 slots at 180 req/s). When full (extreme
	// burst), fast 503 prevents the cascade: blocking semaphore caused proxy
	// goroutines to park waiting for API responses, filling all 128 proxy
	// slots, which blocked new k6 connections → k6 timeouts at 2001ms.
	select {
	case proxySem <- struct{}{}:
		defer func() { <-proxySem }()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(fraudResponses[0])
		return
	}

	// Pick backend via round-robin
	idx := p.counter.Add(1) % uint64(len(p.backends))
	backend := p.backends[idx]

	// 3. Read body into reusable buffer
	bodyBuf := bodyBufPool.Get().(*bytes.Buffer)
	bodyBuf.Reset()
	if _, err := io.Copy(bodyBuf, r.Body); err != nil {
		bodyBufPool.Put(bodyBuf)
		http.Error(w, "cannot read body", http.StatusInternalServerError)
		return
	}
	bodyBytes := bodyBuf.Bytes()

	// 4. Parse JSON (proxy does this — API gets binary)
	jsonPayload := jsonPayloadPool.Get().(*model.TransactionPayload)
	defer jsonPayloadPool.Put(jsonPayload)
	if err := json.Unmarshal(bodyBytes, jsonPayload); err != nil {
		bodyBufPool.Put(bodyBuf)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid JSON"}`))
		return
	}
	bodyBufPool.Put(bodyBuf)

	// 5. Convert to codec.Payload (flat struct for binary encoding)
	cp := codecPayloadPool.Get().(*codec.Payload)
	defer codecPayloadPool.Put(cp)

	cp.Amount = jsonPayload.Transaction.Amount
	cp.Installments = jsonPayload.Transaction.Installments
	cp.RequestedAt = jsonPayload.Transaction.RequestedAt
	cp.AvgAmount = jsonPayload.Customer.AvgAmount
	cp.TxCount24h = jsonPayload.Customer.TxCount24h
	cp.KnownMerchants = jsonPayload.Customer.KnownMerchants
	cp.MerchantID = jsonPayload.Merchant.ID
	cp.MCC = jsonPayload.Merchant.MCC
	cp.MerchantAvgAmount = jsonPayload.Merchant.AvgAmount
	cp.IsOnline = jsonPayload.Terminal.IsOnline
	cp.CardPresent = jsonPayload.Terminal.CardPresent
	cp.KmFromHome = jsonPayload.Terminal.KmFromHome
	cp.HasLastTransaction = jsonPayload.LastTransaction != nil
	if cp.HasLastTransaction {
		cp.LastTimestamp = jsonPayload.LastTransaction.Timestamp
		cp.LastKmFromCurrent = jsonPayload.LastTransaction.KmFromCurrent
	}

	// 6. Encode binary payload into pooled buffer
	binBuf := encodeBufPool.Get().(*bytes.Buffer)
	binBuf.Reset()
	if err := codec.EncodePayload(binBuf, cp); err != nil {
		encodeBufPool.Put(binBuf)
		http.Error(w, "encode error", http.StatusInternalServerError)
		return
	}

	// 7. Build backend request with binary body
	targetURL := backend + r.URL.Path
	breq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, binBuf)
	if err != nil {
		encodeBufPool.Put(binBuf)
		http.Error(w, "cannot create request", http.StatusInternalServerError)
		return
	}
	breq.Header.Set("Content-Type", "application/octet-stream")
	breq.ContentLength = int64(binBuf.Len())

	// 8. Send to backend via http.Client (Unix socket transport)
	resp, err := p.client.Do(breq)
	encodeBufPool.Put(binBuf) // buffer consumed by http.NewRequestWithContext
	if err != nil {
		http.Error(w, "backend error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 9. Check for API-level errors
	if resp.StatusCode != http.StatusOK {
		// Read error body and forward as JSON
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(errBody)
		return
	}

	// 10. Decode binary response (9 bytes)
	binResp, err := codec.DecodeResponse(resp.Body)
	if err != nil {
		http.Error(w, "invalid response", http.StatusBadGateway)
		return
	}

	// 11. Write pre-allocated JSON response (zero serialization)
	fraudCount := int(math.Round(binResp.FraudScore * 5))
	if fraudCount < 0 {
		fraudCount = 0
	} else if fraudCount > 5 {
		fraudCount = 5
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(fraudResponses[fraudCount])
}

// BackendInfo holds metadata for a single backend instance.
type BackendInfo struct {
	RawURL   string
	ReadyURL string
}

// ReadyHandler handles GET /ready by checking /ready on each backend via Unix socket.
type ReadyHandler struct {
	backends []BackendInfo
	client   *http.Client
}

type backendStatus struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

func (h *ReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allOK := true
	results := make([]backendStatus, 0, len(h.backends))

	for _, b := range h.backends {
		resp, err := h.client.Get(b.ReadyURL)
		if err != nil {
			allOK = false
			results = append(results, backendStatus{
				URL:    b.RawURL,
				Status: "unreachable: " + err.Error(),
			})
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			results = append(results, backendStatus{URL: b.RawURL, Status: "ok"})
		} else {
			allOK = false
			results = append(results, backendStatus{
				URL:    b.RawURL,
				Status: http.StatusText(resp.StatusCode),
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if allOK {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"status":   "ok",
			"backends": results,
		})
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{
			"status":   "degraded",
			"backends": results,
		})
	}
}

func main() {
	backendsStr := os.Getenv("BACKENDS")
	if backendsStr == "" {
		backendsStr = "http://api-1:8080,http://api-2:8080"
	}
	backendsList := strings.Split(backendsStr, ",")
	if len(backendsList) < 2 {
		log.Fatalf("At least 2 backends required, got: %d", len(backendsList))
	}

	unixSocketDir := os.Getenv("UNIX_SOCKET_DIR")
	if unixSocketDir == "" {
		unixSocketDir = "/run/sock"
	}

	proxyTransport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     30 * time.Second,
		DialContext:         unixSocketDialer(unixSocketDir),
	}

	var rawBackends []string
	var backendInfos []BackendInfo

	for _, b := range backendsList {
		trimmed := strings.TrimSpace(b)
		rawBackends = append(rawBackends, trimmed)
		backendInfos = append(backendInfos, BackendInfo{
			RawURL:   trimmed,
			ReadyURL: strings.TrimRight(trimmed, "/") + "/ready",
		})
	}

	// Round-robin proxy with binary codec
	proxy := &RoundRobinProxy{
		backends: rawBackends,
		client: &http.Client{
			Transport: proxyTransport,
			Timeout:   500 * time.Millisecond,
		},
	}

	readyClient := &http.Client{
		Transport: proxyTransport,
		Timeout:   1 * time.Second,
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}

	mux := http.NewServeMux()
	mux.Handle("GET /ready", &ReadyHandler{backends: backendInfos, client: readyClient})
	mux.Handle("/", proxy)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 100 * time.Millisecond,
		ReadTimeout:       200 * time.Millisecond,
		WriteTimeout:      200 * time.Millisecond,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4096,
	}

	log.Printf("Proxy starting on port %s, backends: %v\n", port, backendsList)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}
