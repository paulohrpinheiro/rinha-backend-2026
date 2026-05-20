package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var payloads = [][]byte{
	[]byte(`{"id":"a","transaction":{"amount":384.88,"installments":3,"requested_at":"2026-03-11T20:23:35Z"},"customer":{"avg_amount":769.76,"tx_count_24h":3,"known_merchants":["MERC-009","MERC-001"]},"merchant":{"id":"MERC-001","mcc":"5912","avg_amount":298.95},"terminal":{"is_online":false,"card_present":true,"km_from_home":13.7},"last_transaction":{"timestamp":"2026-03-11T14:58:35Z","km_from_current":18.86}}`),
	[]byte(`{"id":"b","transaction":{"amount":2911.41,"installments":12,"requested_at":"2026-03-19T02:17:11Z"},"customer":{"avg_amount":411.03,"tx_count_24h":8,"known_merchants":["MERC-221","MERC-010"]},"merchant":{"id":"MERC-551","mcc":"6011","avg_amount":712.22},"terminal":{"is_online":true,"card_present":false,"km_from_home":2.18},"last_transaction":{"timestamp":"2026-03-18T23:51:05Z","km_from_current":1.34}}`),
	[]byte(`{"id":"c","transaction":{"amount":41.12,"installments":2,"requested_at":"2026-03-11T18:45:53Z"},"customer":{"avg_amount":82.24,"tx_count_24h":3,"known_merchants":["MERC-003","MERC-016"]},"merchant":{"id":"MERC-016","mcc":"5411","avg_amount":60.25},"terminal":{"is_online":false,"card_present":true,"km_from_home":29.2},"last_transaction":null}`),
	[]byte(`{"id":"d","transaction":{"amount":87.91,"installments":1,"requested_at":"2026-03-13T14:31:30Z"},"customer":{"avg_amount":703.28,"tx_count_24h":5,"known_merchants":["MERC-003"]},"merchant":{"id":"MERC-512","mcc":"5814","avg_amount":480.5},"terminal":{"is_online":true,"card_present":false,"km_from_home":799.5},"last_transaction":{"timestamp":"2026-03-13T05:37:37Z","km_from_current":793.78}}`),
}

var (
	target      = flag.String("target", "http://localhost:9999", "URL base")
	duration    = flag.Duration("duration", 5*time.Minute, "duracao total")
	rate        = flag.Int("rate", 180, "req/s em regime")
	rampUp      = flag.Duration("ramp-up", 30*time.Second, "ramp-up")
	concurrency = flag.Int("concurrency", 50, "workers HTTP")
)

func main() {
	flag.Parse()
	url := *target + "/fraud-score"
	if *duration <= *rampUp { fmt.Fprintln(os.Stderr, "duration > ramp-up"); os.Exit(1) }

	fmt.Println("============================================")
	fmt.Println("  Rinha 2026 — Benchmark Realista")
	fmt.Println("============================================")
	fmt.Printf("  Target:   %s\n", url)
	fmt.Printf("  Rate:     %d req/s\n", *rate)
	fmt.Printf("  Duration: %s (ramp-up %s + steady %s)\n", *duration, *rampUp, *duration-*rampUp)
	fmt.Printf("  Workers:  %d\n", *concurrency)

	// Aguardar /ready
	hc := &http.Client{Timeout: 2 * time.Second}
	for i := 0; i < 60; i++ {
		r, e := hc.Get(*target + "/ready")
		if e == nil && r.StatusCode == 200 { r.Body.Close(); break }
		if r != nil { r.Body.Close() }
		if i == 59 { fmt.Println("\n[ERRO] /ready"); os.Exit(1) }
		time.Sleep(1 * time.Second)
	}
	fmt.Println("  /ready:   OK\n")

	// Métricas
	type sample struct {
		latency time.Duration
		status  int
		isErr   bool
	}
	var (
		mu         sync.Mutex
		samples    []sample
		totalReqs  atomic.Int64
		httpErrors atomic.Int64
		connErrors atomic.Int64
		sentReqs   atomic.Int64
	)

	transport := &http.Transport{MaxIdleConns: 100, MaxIdleConnsPerHost: 50, IdleConnTimeout: 30 * time.Second}
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	jobs := make(chan int, *concurrency*4)

	// Workers
	var wg sync.WaitGroup
	for w := 0; w < *concurrency; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pi := range jobs {
				start := time.Now()
				req, _ := http.NewRequest("POST", url, bytes.NewReader(payloads[pi]))
				req.Header.Set("Content-Type", "application/json")
				resp, err := client.Do(req)
				elapsed := time.Since(start)
				totalReqs.Add(1)
				s := sample{latency: elapsed}
				if err != nil { s.isErr = true; connErrors.Add(1) } else { s.status = resp.StatusCode; io.Copy(io.Discard, resp.Body); resp.Body.Close(); if resp.StatusCode != 200 { httpErrors.Add(1) } }
				mu.Lock(); samples = append(samples, s); mu.Unlock()
			}
		}()
	}

	// Gerador: envia N jobs a cada 100ms, baseado na taxa atual
	startTime := time.Now()
	deadline := startTime.Add(*duration)
	rampEnd := startTime.Add(*rampUp)
	var seq int64
	progressTicker := time.NewTicker(10 * time.Second)
	defer progressTicker.Stop()
	rateTicker := time.NewTicker(100 * time.Millisecond)
	defer rateTicker.Stop()

	fmt.Printf("  [\\] Iniciando carga...\n")

	go func() {
		for time.Now().Before(deadline) {
			<-rateTicker.C
			now := time.Now()
			elapsed := now.Sub(startTime)
			var currentRate float64
			if now.Before(rampEnd) {
				progress := float64(elapsed) / float64(*rampUp)
				if progress > 1 { progress = 1 }
				currentRate = float64(*rate) * progress
			} else {
				currentRate = float64(*rate)
			}
			// Quantos jobs neste tick de 100ms?
			n := int(currentRate * 0.1)
			for i := 0; i < n; i++ {
				jobs <- int(seq % 4)
				seq++
				sentReqs.Add(1)
			}
		}
		close(jobs)
	}()

	// Aguarda deadline, mostrando progresso
	for time.Now().Before(deadline) {
		<-progressTicker.C
		remaining := time.Until(deadline)
		fmt.Printf("  [%s] enviadas=%d processadas=%d restantes=%s\n",
			time.Since(startTime).Round(time.Second), sentReqs.Load(), totalReqs.Load(), remaining.Round(time.Second))
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	// Resultados
	fmt.Println("\n============================================")
	fmt.Println("  Resultados")
	fmt.Println("============================================")
	total := totalReqs.Load(); errs := httpErrors.Load() + connErrors.Load(); ok := total - errs
	successRate := float64(ok) / float64(total) * 100

	mu.Lock(); lats := make([]time.Duration, len(samples))
	for i, s := range samples { lats[i] = s.latency }
	mu.Unlock()
	sort.Slice(lats, func(i, j int) bool { return lats[i] < lats[j] })
	n := len(lats)
	if n == 0 { fmt.Println("Zero amostras"); os.Exit(1) }

	p50, p95, p99 := lats[n*50/100], lats[n*95/100], lats[n*99/100]
	var sum time.Duration; for _, l := range lats { sum += l }
	avg := sum / time.Duration(n)
	tp := float64(total) / elapsed.Seconds()

	fmt.Printf("  Duracao:       %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("  Requisicoes:   %d\n", total)
	fmt.Printf("  OK (200):      %d (%.1f%%)\n", ok, successRate)
	fmt.Printf("  Erros HTTP:    %d\n", httpErrors.Load())
	fmt.Printf("  Erros conexao: %d\n", connErrors.Load())
	fmt.Printf("  Throughput:    %.1f req/s\n", tp)
	fmt.Printf("  Latencia avg:  %s\n", avg.Round(time.Microsecond))
	fmt.Printf("  Latencia p50:  %s\n", p50.Round(time.Microsecond))
	fmt.Printf("  Latencia p95:  %s\n", p95.Round(time.Microsecond))
	fmt.Printf("  Latencia p99:  %s\n", p99.Round(time.Microsecond))

	// /debug/vars
	fmt.Println("\n============================================")
	fmt.Println("  Contadores /debug/vars")
	fmt.Println("============================================")
	if r, e := http.Get(*target + "/debug/vars"); e == nil { b, _ := io.ReadAll(r.Body); r.Body.Close(); fmt.Printf("  %s\n", b) } else { fmt.Printf("  indisponivel\n") }

	// Julgamento
	fmt.Println("============================================")
	fmt.Println("  Analise")
	fmt.Println("============================================")
	passed := true
	if p99 > 2000*time.Millisecond { fmt.Printf("  ❌ p99=%s > 2000ms → corte -3000\n", p99.Round(time.Millisecond)); passed = false } else { fmt.Printf("  ✅ p99=%s < 2000ms\n", p99.Round(time.Millisecond)) }
	if successRate < 85.0 { fmt.Printf("  ❌ taxa=%.1f%% < 85%% → corte -3000\n", successRate); passed = false } else { fmt.Printf("  ✅ taxa=%.1f%% >= 85%%\n", successRate) }
	if connErrors.Load() > int64(float64(total)*0.05) { fmt.Printf("  ⚠️  erros conexao=%d (>5%%)\n", connErrors.Load()) }
	if passed { fmt.Println("\n  🟢 BENCHMARK PASSOU — pode submeter") } else { fmt.Println("\n  🔴 BENCHMARK FALHOU — reverter/ajustar") }
}
