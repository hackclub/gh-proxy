#!/bin/bash

API_KEY="$1"
TARGET_RPS="$2"
DURATION="$3"

if [ -z "$API_KEY" ] || [ -z "$TARGET_RPS" ] || [ -z "$DURATION" ]; then
    echo "Usage: $0 <api-key> <target-rps> <duration-seconds>"
    exit 1
fi

echo "🚀 Starting $DURATION second benchmark targeting $TARGET_RPS RPS..."

# Calculate requests per worker per second
WORKERS=10
REQUESTS_PER_WORKER_PER_SEC=$((TARGET_RPS / WORKERS))
DELAY_BETWEEN_REQUESTS=$(echo "scale=3; 1.0 / $REQUESTS_PER_WORKER_PER_SEC" | bc)

echo "   Workers: $WORKERS"
echo "   Requests per worker per second: $REQUESTS_PER_WORKER_PER_SEC"
echo "   Delay between requests: ${DELAY_BETWEEN_REQUESTS}s"

START_TIME=$(date +%s)
TOTAL_REQUESTS=0
SUCCESSFUL=0
ERRORS=0
CACHE_HITS=0
CACHE_MISSES=0

# Start worker processes
for worker in $(seq 1 $WORKERS); do
    (
        END_TIME=$((START_TIME + DURATION))
        while [ $(date +%s) -lt $END_TIME ]; do
            if RESPONSE=$(curl -s -H "X-API-Key: $API_KEY" -D /tmp/headers_$worker.tmp "http://localhost:8080/gh/repos/zachlatta/sshtron" 2>/dev/null); then
                if grep -q "HTTP/1.1 200" /tmp/headers_$worker.tmp 2>/dev/null; then
                    echo "S" >> /tmp/success_$worker.log
                    if grep -q "X-Gh-Proxy-Cache: hit" /tmp/headers_$worker.tmp 2>/dev/null; then
                        echo "H" >> /tmp/cache_hits_$worker.log
                    else
                        echo "M" >> /tmp/cache_miss_$worker.log
                    fi
                else
                    echo "E" >> /tmp/error_$worker.log
                fi
            else
                echo "E" >> /tmp/error_$worker.log
            fi
            
            echo "R" >> /tmp/total_$worker.log
            sleep $DELAY_BETWEEN_REQUESTS
        done
    ) &
done

# Wait for all workers to complete
wait

# Calculate results
ACTUAL_DURATION=$(($(date +%s) - START_TIME))

for worker in $(seq 1 $WORKERS); do
    WORKER_TOTAL=$(wc -l < /tmp/total_$worker.log 2>/dev/null || echo 0)
    WORKER_SUCCESS=$(wc -l < /tmp/success_$worker.log 2>/dev/null || echo 0)
    WORKER_ERRORS=$(wc -l < /tmp/error_$worker.log 2>/dev/null || echo 0)
    WORKER_HITS=$(wc -l < /tmp/cache_hits_$worker.log 2>/dev/null || echo 0)
    WORKER_MISSES=$(wc -l < /tmp/cache_miss_$worker.log 2>/dev/null || echo 0)
    
    TOTAL_REQUESTS=$((TOTAL_REQUESTS + WORKER_TOTAL))
    SUCCESSFUL=$((SUCCESSFUL + WORKER_SUCCESS))
    ERRORS=$((ERRORS + WORKER_ERRORS))
    CACHE_HITS=$((CACHE_HITS + WORKER_HITS))
    CACHE_MISSES=$((CACHE_MISSES + WORKER_MISSES))
done

# Calculate metrics
ACTUAL_RPS=$((TOTAL_REQUESTS / ACTUAL_DURATION))
SUCCESS_RATE=$((SUCCESSFUL * 100 / TOTAL_REQUESTS))
CACHE_HIT_RATE=$((CACHE_HITS * 100 / (CACHE_HITS + CACHE_MISSES)))

echo ""
echo "📊 BENCHMARK RESULTS:"
echo "====================="
echo "Duration: ${ACTUAL_DURATION}s"
echo "Total Requests: $TOTAL_REQUESTS"
echo "Successful: $SUCCESSFUL ($SUCCESS_RATE%)"
echo "Errors: $ERRORS"
echo "Cache Hits: $CACHE_HITS ($CACHE_HIT_RATE%)"
echo "Cache Misses: $CACHE_MISSES"
echo "Actual RPS: $ACTUAL_RPS"

if [ $ACTUAL_RPS -ge $TARGET_RPS ]; then
    echo "✅ SUCCESS: Achieved $ACTUAL_RPS RPS (target: $TARGET_RPS)"
else
    echo "❌ MISSED TARGET: $ACTUAL_RPS RPS (target: $TARGET_RPS)"
fi

# Cleanup
rm -f /tmp/total_*.log /tmp/success_*.log /tmp/error_*.log /tmp/cache_*.log /tmp/headers_*.tmp 2>/dev/null

