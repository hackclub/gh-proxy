package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	if len(os.Args) < 4 {
		log.Fatal("Usage: benchmark-fast <url> <api-key> <target-rps> [duration-seconds]")
	}

	baseURL := os.Args[1]
	apiKey := os.Args[2] 
	targetRPS := 200
	duration := 60

	fmt.Sscanf(os.Args[3], "%d", &targetRPS)
	if len(os.Args) > 4 {
		fmt.Sscanf(os.Args[4], "%d", &duration)
	}

	fmt.Printf("🚀 HIGH-THROUGHPUT BENCHMARK\n")
	fmt.Printf("============================\n")
	fmt.Printf("URL: %s\n", baseURL)
	fmt.Printf("Target RPS: %d\n", targetRPS)
	fmt.Printf("Duration: %d seconds\n", duration)
	fmt.Printf("Strategy: Aggressive batching with minimal delays\n\n")

	result := runAggressiveBenchmark(baseURL, apiKey, targetRPS, duration)
	
	fmt.Printf("📊 FINAL RESULTS:\n")
	fmt.Printf("=================\n")
	fmt.Printf("Duration: %ds\n", result.Duration)
	fmt.Printf("Total Requests: %d\n", result.TotalRequests)
	fmt.Printf("Successful: %d (%.1f%%)\n", result.SuccessRequests, float64(result.SuccessRequests)*100/float64(result.TotalRequests))
	fmt.Printf("Errors: %d\n", result.ErrorRequests)
	fmt.Printf("Cache Hits: %d\n", result.CacheHits)
	fmt.Printf("Cache Misses: %d\n", result.CacheMisses)
	fmt.Printf("Actual RPS: %.1f\n", result.ActualRPS)
	fmt.Printf("Avg Latency: %.1fms\n", float64(result.AvgLatency)/float64(time.Millisecond))

	if result.ActualRPS >= float64(targetRPS) {
		fmt.Printf("\n✅ SUCCESS: Achieved %.1f RPS (target: %d)\n", result.ActualRPS, targetRPS)
	} else {
		fmt.Printf("\n❌ MISSED TARGET: %.1f RPS (target: %d)\n", result.ActualRPS, targetRPS)
	}
}

type BenchResult struct {
	Duration        int
	TotalRequests   int64
	SuccessRequests int64
	ErrorRequests   int64
	CacheHits       int64
	CacheMisses     int64
	ActualRPS       float64
	AvgLatency      time.Duration
}

func runAggressiveBenchmark(baseURL, apiKey string, targetRPS, duration int) BenchResult {
	var (
		totalRequests   int64
		successRequests int64
		errorRequests   int64
		cacheHits       int64
		cacheMisses     int64
		totalLatency    int64
	)

	// Aggressive HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:          200,
			MaxIdleConnsPerHost:   50,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   2 * time.Second,
			ResponseHeaderTimeout: 3 * time.Second,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(duration)*time.Second)
	defer cancel()

	// Calculate workers needed for target RPS
	workers := targetRPS / 10 // Each worker targets ~10 RPS
	if workers < 10 {
		workers = 10
	}
	if workers > 100 {
		workers = 100
	}

	fmt.Printf("🏃 Starting %d workers targeting %d RPS...\n\n", workers, targetRPS)

	var wg sync.WaitGroup
	start := time.Now()

	// Single endpoint for consistent caching
	endpoint := baseURL + "/gh/repos/zachlatta/sshtron"

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			// Calculate delay for this worker to hit target RPS
			workerRPS := targetRPS / workers
			delay := time.Duration(1000/workerRPS) * time.Millisecond
			
			for {
				select {
				case <-ctx.Done():
					return
				default:
					reqStart := time.Now()
					
					req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
					if err != nil {
						atomic.AddInt64(&errorRequests, 1)
						atomic.AddInt64(&totalRequests, 1)
						continue
					}
					
					req.Header.Set("X-API-Key", apiKey)
					resp, err := client.Do(req)
					
					latency := time.Since(reqStart)
					atomic.AddInt64(&totalLatency, int64(latency))
					atomic.AddInt64(&totalRequests, 1)
					
					if err != nil {
						atomic.AddInt64(&errorRequests, 1)
						continue
					}
					
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						atomic.AddInt64(&successRequests, 1)
						
						// Check cache status
						if resp.Header.Get("X-Gh-Proxy-Cache") == "hit" {
							atomic.AddInt64(&cacheHits, 1)
						} else {
							atomic.AddInt64(&cacheMisses, 1)
						}
					} else {
						atomic.AddInt64(&errorRequests, 1)
					}
					
					// Consume response
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
					
					// Rate limiting delay
					time.Sleep(delay)
				}
			}
		}(i)
	}

	// Monitor progress
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				elapsed := time.Since(start)
				current := atomic.LoadInt64(&totalRequests)
				currentRPS := float64(current) / elapsed.Seconds()
				fmt.Printf("⏱️  %v elapsed: %d requests (%.1f RPS)\n", elapsed.Truncate(time.Second), current, currentRPS)
			}
		}
	}()

	wg.Wait()
	actualDuration := time.Since(start)

	total := atomic.LoadInt64(&totalRequests)
	avgLatency := time.Duration(0)
	if total > 0 {
		avgLatency = time.Duration(atomic.LoadInt64(&totalLatency) / total)
	}

	return BenchResult{
		Duration:        int(actualDuration.Seconds()),
		TotalRequests:   total,
		SuccessRequests: atomic.LoadInt64(&successRequests),
		ErrorRequests:   atomic.LoadInt64(&errorRequests),
		CacheHits:       atomic.LoadInt64(&cacheHits),
		CacheMisses:     atomic.LoadInt64(&cacheMisses),
		ActualRPS:       float64(total) / actualDuration.Seconds(),
		AvgLatency:      avgLatency,
	}
}
