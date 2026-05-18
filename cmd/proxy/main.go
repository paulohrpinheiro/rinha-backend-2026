// Load balancer proxy using direct http.Client (no httputil.ReverseProxy).
// Distributes POST /fraud-score in round-robin to API instances via Unix sockets.
// Serves GET /ready locally by checking health of all backends.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// unixSocketDialer returns a dialer that connects to /run/sock/<hostname>.sock
// instead of TCP, eliminating TCP/IP overhead between proxy and API containers.
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

// bodyBufPool reuses byte buffers for reading request bodies,
// avoiding allocation per request (~300 bytes each).
var bodyBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// proxySem limits concurrent proxy requests to prevent goroutine explosion.
// 64 is generous: with ~0.15ms per request and 0.10 CPU (100ms/s),
// this allows up to ~42k req/s sustained throughput.
var proxySem = make(chan struct{}, 64)

// RoundRobinProxy is a minimal reverse proxy without the overhead of
// httputil.ReverseProxy. It creates a fresh backend request, copies only
// the Content-Type header, and streams the response back.
type RoundRobinProxy struct {
	backends []string
	counter  atomic.Uint64
	client   *http.Client
}

func (p *RoundRobinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 1. Acquire semaphore slot; return 503 immediately if at capacity
	select {
	case proxySem <- struct{}{}:
		defer func() { <-proxySem }()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"too many requests"}`))
		return
	}

	// 2. Pick backend via round-robin
	idx := p.counter.Add(1) % uint64(len(p.backends))
	backend := p.backends[idx]

	// 3. Read body into reusable buffer (avoids allocation per request)
	bodyBuf := bodyBufPool.Get().(*bytes.Buffer)
	bodyBuf.Reset()
	if _, err := io.Copy(bodyBuf, r.Body); err != nil {
		bodyBufPool.Put(bodyBuf)
		http.Error(w, "cannot read body", http.StatusInternalServerError)
		return
	}
	body := bodyBuf.Bytes()

	// 4. Build backend URL from base URL + request path
	targetURL := backend + r.URL.Path
	breq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(body))
	if err != nil {
		bodyBufPool.Put(bodyBuf)
		http.Error(w, "cannot create request", http.StatusInternalServerError)
		return
	}

	// 5. Forward only Content-Type — API doesn't need other headers
	breq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	breq.ContentLength = int64(len(body))

	// 6. Send to backend via http.Client (Unix socket transport)
	resp, err := p.client.Do(breq)
	// Body buffer is no longer needed after client.Do reads the reader
	bodyBufPool.Put(bodyBuf)
	if err != nil {
		http.Error(w, "backend error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 7. Forward response — only Content-Type header (avoids copying Date, Content-Length, etc.)
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// BackendInfo holds metadata for a single backend instance used by ReadyHandler.
type BackendInfo struct {
	RawURL   string
	ReadyURL string
}

// ReadyHandler handles GET /ready by checking /ready on each backend via Unix socket.
type ReadyHandler struct {
	backends []BackendInfo
	client   *http.Client
}

// backendStatus is a JSON-serializable status for one backend.
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

	// Shared transport for both proxy and ready checks
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

	// Round-robin proxy: http.Client with Unix socket transport
	proxy := &RoundRobinProxy{
		backends: rawBackends,
		client: &http.Client{
			Transport: proxyTransport,
			Timeout:   1 * time.Second,
		},
	}

	// Ready checks use the same transport
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
		ReadHeaderTimeout: 500 * time.Millisecond,
		ReadTimeout:       1 * time.Second,
		WriteTimeout:      1 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4096,
	}

	log.Printf("Proxy starting on port %s, backends: %v\n", port, backendsList)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}