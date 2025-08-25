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

type BenchmarkResult struct {
	TotalRequests   int64
	SuccessRequests int64
	ErrorRequests   int64
	TotalDuration   time.Duration
	CacheHits       int64
	CacheMisses     int64
	AvgResponseTime time.Duration
	RPS             float64
}

func main() {
	if len(os.Args) < 3 {
		log.Fatal("Usage: benchmark <url> <api-key> [concurrent-users] [duration-seconds]")
	}

	baseURL := os.Args[1]
	apiKey := os.Args[2]
	
	concurrentUsers := 50
	durationSeconds := 30
	if len(os.Args) > 3 {
		fmt.Sscanf(os.Args[3], "%d", &concurrentUsers)
	}
	if len(os.Args) > 4 {
		fmt.Sscanf(os.Args[4], "%d", &durationSeconds)
	}

	log.Printf("🚀 Starting benchmark:")
	log.Printf("   URL: %s", baseURL)
	log.Printf("   Concurrent users: %d", concurrentUsers)
	log.Printf("   Duration: %d seconds", durationSeconds)
	log.Printf("   Target: 500+ RPS")

	result := runBenchmark(baseURL, apiKey, concurrentUsers, time.Duration(durationSeconds)*time.Second)
	
	fmt.Printf("\n")
	fmt.Printf("📊 BENCHMARK RESULTS\n")
	fmt.Printf("====================\n")
	fmt.Printf("Total Requests:     %d\n", result.TotalRequests)
	fmt.Printf("Successful:         %d (%.1f%%)\n", result.SuccessRequests, float64(result.SuccessRequests)*100/float64(result.TotalRequests))
	fmt.Printf("Errors:             %d (%.1f%%)\n", result.ErrorRequests, float64(result.ErrorRequests)*100/float64(result.TotalRequests))
	fmt.Printf("Cache Hits:         %d (%.1f%%)\n", result.CacheHits, float64(result.CacheHits)*100/float64(result.SuccessRequests))
	fmt.Printf("Cache Misses:       %d (%.1f%%)\n", result.CacheMisses, float64(result.CacheMisses)*100/float64(result.SuccessRequests))
	fmt.Printf("Duration:           %v\n", result.TotalDuration.Truncate(time.Millisecond))
	fmt.Printf("Avg Response Time:  %v\n", result.AvgResponseTime.Truncate(time.Millisecond))
	fmt.Printf("Requests/Second:    %.1f RPS\n", result.RPS)
	
	fmt.Printf("\n")
	if result.RPS >= 500 {
		fmt.Printf("✅ SUCCESS: Achieved %.1f RPS (target: 500+ RPS)\n", result.RPS)
	} else {
		fmt.Printf("❌ FAILED: Only achieved %.1f RPS (target: 500+ RPS)\n", result.RPS)
	}
	
	if result.SuccessRequests < result.TotalRequests/2 {
		fmt.Printf("⚠️  High error rate: %.1f%% errors\n", float64(result.ErrorRequests)*100/float64(result.TotalRequests))
	}
}

func runBenchmark(baseURL, apiKey string, concurrentUsers int, duration time.Duration) BenchmarkResult {
	var (
		totalRequests   int64
		successRequests int64
		errorRequests   int64
		cacheHits       int64
		cacheMisses     int64
		totalResponseTime int64
	)

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()

	var wg sync.WaitGroup
	start := time.Now()

	// Different endpoints to test various scenarios
	endpoints := []string{
		"/gh/user",
		"/gh/repos/zachlatta/sshtron", 
		"/gh/repos/zachlatta/sshtron/languages",
		"/gh/repos/zachlatta/sshtron/contributors",
		"/gh/users/zachlatta",
		"/gh/users/zachlatta/repos",
	}

	// Start concurrent workers
	for i := 0; i < concurrentUsers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			endpointIdx := 0
			
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Cycle through endpoints
					endpoint := endpoints[endpointIdx%len(endpoints)]
					endpointIdx++
					
					reqStart := time.Now()
					req, _ := http.NewRequestWithContext(ctx, "GET", baseURL+endpoint, nil)
					req.Header.Set("X-API-Key", apiKey)
					
					resp, err := client.Do(req)
					
					// Add small delay to prevent overwhelming the server during testing
					time.Sleep(10 * time.Millisecond)
					responseTime := time.Since(reqStart)
					
					atomic.AddInt64(&totalRequests, 1)
					atomic.AddInt64(&totalResponseTime, int64(responseTime))
					
					if err != nil {
						atomic.AddInt64(&errorRequests, 1)
						continue
					}
					
					if resp.StatusCode >= 200 && resp.StatusCode < 300 {
						atomic.AddInt64(&successRequests, 1)
						
						// Check cache status
						cacheStatus := resp.Header.Get("X-Gh-Proxy-Cache")
						if cacheStatus == "hit" {
							atomic.AddInt64(&cacheHits, 1)
						} else if cacheStatus == "miss" {
							atomic.AddInt64(&cacheMisses, 1)
						}
					} else {
						atomic.AddInt64(&errorRequests, 1)
					}
					
					// Read and discard response body
					io.Copy(io.Discard, resp.Body)
					resp.Body.Close()
				}
			}
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(start)

	avgResponseTime := time.Duration(0)
	if totalRequests > 0 {
		avgResponseTime = time.Duration(atomic.LoadInt64(&totalResponseTime) / atomic.LoadInt64(&totalRequests))
	}

	rps := float64(atomic.LoadInt64(&totalRequests)) / totalDuration.Seconds()

	return BenchmarkResult{
		TotalRequests:   atomic.LoadInt64(&totalRequests),
		SuccessRequests: atomic.LoadInt64(&successRequests),
		ErrorRequests:   atomic.LoadInt64(&errorRequests),
		TotalDuration:   totalDuration,
		CacheHits:       atomic.LoadInt64(&cacheHits),
		CacheMisses:     atomic.LoadInt64(&cacheMisses),
		AvgResponseTime: avgResponseTime,
		RPS:             rps,
	}
}
