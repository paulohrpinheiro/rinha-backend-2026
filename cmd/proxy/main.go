// Load balancer proxy that distributes requests in round-robin to API instances.
package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"

	"strings"
)

// RoundRobinProxy is a simple round-robin HTTP reverse proxy.
type RoundRobinProxy struct {
	backends []*httputil.ReverseProxy
	counter  atomic.Uint64
}

func (p *RoundRobinProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	idx := p.counter.Add(1) % uint64(len(p.backends))
	p.backends[idx].ServeHTTP(w, r)
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

	proxy := &RoundRobinProxy{
		backends: make([]*httputil.ReverseProxy, len(backendsList)),
	}
	for i, b := range backendsList {
		proxy.backends[i] = httputil.NewSingleHostReverseProxy(mustParseURL(strings.TrimSpace(b)))
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "9999"
	}

	log.Printf("Proxy starting on port %s, backends: %v\n", port, backendsList)
	if err := http.ListenAndServe(":"+port, proxy); err != nil {
		log.Fatalf("Proxy error: %v", err)
	}
}