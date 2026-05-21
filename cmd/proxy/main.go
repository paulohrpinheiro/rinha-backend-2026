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
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"rinha-backend/internal/codec"
)

// fraudResponses holds the 8 possible JSON responses pre-computed.
// Zero serialization in the hot path — just index by fraud count (K=7).
// fraudScore = fraudCount/7 → 0.0, 0.143, 0.286, 0.429, 0.571, 0.714, 0.857, 1.0
var fraudResponses = [8][]byte{
	[]byte(`{"approved":true,"fraud_score":0.0}`),
	[]byte(`{"approved":true,"fraud_score":0.14285714285714285}`),
	[]byte(`{"approved":true,"fraud_score":0.2857142857142857}`),
	[]byte(`{"approved":true,"fraud_score":0.42857142857142855}`),
	[]byte(`{"approved":true,"fraud_score":0.5714285714285714}`),
	[]byte(`{"approved":false,"fraud_score":0.7142857142857143}`),
	[]byte(`{"approved":false,"fraud_score":0.8571428571428571}`),
	[]byte(`{"approved":false,"fraud_score":1.0}`),
}

// ProxyCounters tracks internal metrics for diagnosing where requests are lost.
// All counters use atomic.Int64 for lock-free increment under concurrency.
type ProxyCounters struct {
	RequestsReceived  atomic.Int64 // total accepted by proxy
	RequestsForwarded atomic.Int64 // successfully sent to API
	ResponsesReceived atomic.Int64 // 200 OK responses from API
	BackendErrors     atomic.Int64 // client.Do returned error (timeout/connection)
	APIErrors         atomic.Int64 // API responded with non-200 status
	DecodeErrors      atomic.Int64 // codec.DecodeResponse failed
	Semaphore503s     atomic.Int64 // semaphore full → 503
	ParseErrors       atomic.Int64 // invalid JSON in request body
	EncodeErrors      atomic.Int64 // codec.EncodePayload failed
	ReadErrors        atomic.Int64 // io.Copy body read failed
	BadGatewayErrors  atomic.Int64 // proxy→API request creation failed
}

var proxyCounters ProxyCounters

// debugVarsHandler exposes internal counters as JSON for diagnostics.
func debugVarsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"proxy":{`+
		`"requests_received":%d,`+
		`"requests_forwarded":%d,`+
		`"responses_received":%d,`+
		`"backend_errors":%d,`+
		`"api_errors":%d,`+
		`"decode_errors":%d,`+
		`"semaphore_503s":%d,`+
		`"parse_errors":%d,`+
		`"encode_errors":%d,`+
		`"read_errors":%d,`+
		`"bad_gateway_errors":%d`+
		`}}`+"\n",
		proxyCounters.RequestsReceived.Load(),
		proxyCounters.RequestsForwarded.Load(),
		proxyCounters.ResponsesReceived.Load(),
		proxyCounters.BackendErrors.Load(),
		proxyCounters.APIErrors.Load(),
		proxyCounters.DecodeErrors.Load(),
		proxyCounters.Semaphore503s.Load(),
		proxyCounters.ParseErrors.Load(),
		proxyCounters.EncodeErrors.Load(),
		proxyCounters.ReadErrors.Load(),
		proxyCounters.BadGatewayErrors.Load(),
	)
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




// proxySem limits concurrent proxy requests (non-blocking, 256 slots).
// 256 slots provides enough headroom for bursts while keeping scheduler
// pressure low with GOMAXPROCS=1. At steady state (~25 concurrent),
// max queue depth is ~33ms (256 × 130μs).
var proxySem = make(chan struct{}, 1024)

// RoundRobinProxy handles POST /fraud-score: parses JSON, encodes to binary,
// forwards to an API, decodes binary response, returns JSON.
type RoundRobinProxy struct {
	backends []string
	counter  atomic.Uint64
	client   *http.Client
}

func (p *RoundRobinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	proxyCounters.RequestsReceived.Add(1)

	// Acquire non-blocking semaphore slot. 256 capacity under normal load
	// (180 req/s) means ~20-30 concurrent — plenty of headroom. When full
	// (extreme burst), fast 503 prevents cascade.
	select {
	case proxySem <- struct{}{}:
		defer func() { <-proxySem }()
	default:
		proxyCounters.Semaphore503s.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write(fraudResponses[0])
		return
	}

	// Pick backend via round-robin
	idx := p.counter.Add(1) % uint64(len(p.backends))
	backend := p.backends[idx]

		// 3. Read body into reusable buffer (forward JSON, zero parsing)
	bodyBuf := bodyBufPool.Get().(*bytes.Buffer)
	bodyBuf.Reset()
	if _, err := io.Copy(bodyBuf, r.Body); err != nil {
		proxyCounters.ReadErrors.Add(1)
		bodyBufPool.Put(bodyBuf)
		http.Error(w, "cannot read body", http.StatusInternalServerError)
		return
	}

	// 4. Build backend request with raw JSON body
	targetURL := backend + r.URL.Path
	breq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bodyBuf)
	if err != nil {
		proxyCounters.BadGatewayErrors.Add(1)
		bodyBufPool.Put(bodyBuf)
		http.Error(w, "cannot create request", http.StatusInternalServerError)
		return
	}
	breq.Header.Set("Content-Type", "application/json")
	breq.ContentLength = int64(bodyBuf.Len())

	// 5. Send to backend
	proxyCounters.RequestsForwarded.Add(1)
	resp, err := p.client.Do(breq)
	bodyBufPool.Put(bodyBuf)
	if err != nil {
		proxyCounters.BackendErrors.Add(1)
		http.Error(w, "backend error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 6. Check for API-level errors
	if resp.StatusCode != http.StatusOK {
		proxyCounters.APIErrors.Add(1)
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(errBody)
		return
	}

	// 7. Decode binary response (9 bytes)
	binResp, err := codec.DecodeResponse(resp.Body)
	if err != nil {
		proxyCounters.DecodeErrors.Add(1)
		http.Error(w, "invalid response", http.StatusBadGateway)
		return
	}

	proxyCounters.ResponsesReceived.Add(1)

	fraudCount := int(math.Round(binResp.FraudScore * 7))
	if fraudCount < 0 {
		fraudCount = 0
	} else if fraudCount > 7 {
		fraudCount = 7
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
			Timeout:   200 * time.Millisecond,
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
	mux.HandleFunc("GET /debug/vars", debugVarsHandler)
	mux.Handle("/", proxy)

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 200 * time.Millisecond,
		ReadTimeout:       200 * time.Millisecond,
		WriteTimeout:      200 * time.Millisecond,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4096,
	}

	log.Printf("Proxy starting on port %s, backends: %v (GOMAXPROCS=%d)\n",
		port, backendsList, runtime.GOMAXPROCS(0))
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}
