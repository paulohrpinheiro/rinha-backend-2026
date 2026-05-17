// Load balancer proxy that distributes requests in round-robin to API instances.
// Serves GET /ready locally by checking health of all backends.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// unixSocketDialer returns a net.Dialer that connects to Unix sockets
// at /run/sock/<hostname>.sock instead of TCP. This eliminates the
// TCP/IP overhead between proxy and API containers.
//
// The ReverseProxy constructs requests using the backend URL (e.g.
// http://api-1:8080), but we override the dial to go through Unix
// sockets. The hostname from the URL is extracted to determine the
// correct socket file.
func unixSocketDialer(socketDir string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		// addr is like "api-1:8080" — extract hostname for socket name
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			host = addr
		}
		socketPath := socketDir + "/" + host + ".sock"
		return net.Dial("unix", socketPath)
	}
}

// RoundRobinProxy is a simple round-robin HTTP reverse proxy.
type RoundRobinProxy struct {
	backends []*httputil.ReverseProxy
	counter  atomic.Uint64
}

func (p *RoundRobinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idx := p.counter.Add(1) % uint64(len(p.backends))
	p.backends[idx].ServeHTTP(w, r)
}

// BackendInfo holds metadata for a single backend instance.
type BackendInfo struct {
	RawURL   string
	ReadyURL string
}

// ReadyHandler handles GET /ready by checking health of all backends.
type ReadyHandler struct {
	backends []BackendInfo
}

// backendStatus is a JSON-serializable status for one backend.
type backendStatus struct {
	URL    string `json:"url"`
	Status string `json:"status"`
}

func (h *ReadyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{Timeout: 1 * time.Second}

	allOK := true
	results := make([]backendStatus, 0, len(h.backends))

	for _, b := range h.backends {
		resp, err := client.Get(b.ReadyURL)
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
			results = append(results, backendStatus{
				URL:    b.RawURL,
				Status: "ok",
			})
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

func mustParseURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil {
		log.Fatalf("Invalid URL %q: %v", raw, err)
	}
	return u
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

	// Build reverse proxies for round-robin forwarding
	proxy := &RoundRobinProxy{
		backends: make([]*httputil.ReverseProxy, len(backendsList)),
	}
	backendInfos := make([]BackendInfo, 0, len(backendsList))

	// Unix socket directory for proxy↔API communication
	unixSocketDir := os.Getenv("UNIX_SOCKET_DIR")
	if unixSocketDir == "" {
		unixSocketDir = "/run/sock"
	}

	// Configure HTTP transport with Unix sockets and connection pooling
	// Unix sockets eliminate TCP/IP overhead between proxy and APIs,
	// reducing per-request latency from ~100μs to <10μs.
	proxyTransport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     30 * time.Second,
		DialContext:         unixSocketDialer(unixSocketDir),
	}

	for i, b := range backendsList {
		trimmed := strings.TrimSpace(b)
		backendInfos = append(backendInfos, BackendInfo{
			RawURL:   trimmed,
			ReadyURL: strings.TrimRight(trimmed, "/") + "/ready",
		})
		rp := httputil.NewSingleHostReverseProxy(mustParseURL(trimmed))
		rp.Transport = proxyTransport
		proxy.backends[i] = rp
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}

	// Route: GET /ready is handled locally; everything else via round-robin.
	mux := http.NewServeMux()
	mux.Handle("GET /ready", &ReadyHandler{backends: backendInfos})
	mux.Handle("/", proxy)

	log.Printf("Proxy starting on port %s, backends: %v\n", port, backendsList)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}
