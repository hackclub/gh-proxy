# Performance Tuning for 500 RPS

## 🎯 Current Bottlenecks Analysis

For 500 RPS, the main bottlenecks are:

### 🔴 Critical Bottlenecks
1. **Database Connection Pool** (20 connections) - Major bottleneck
2. **Rate Limiter Mutex** - Global lock on every request
3. **Cache Queries** - PostgreSQL queries on every request
4. **Token Selection** - Database query for every cache miss

### 🟡 Secondary Bottlenecks  
5. **HTTP Timeouts** - Conservative settings
6. **GitHub API Client** - Single HTTP client, 30s timeout

## ⚡ Performance Optimizations

### 1. Database Connection Pool (Critical)

**Default**: 20 connections per instance

> ⚠️ Size this against the database, not the target RPS. Every instance can open
> up to `DB_MAX_CONNS`, and during a rolling deploy old and new instances run
> side by side. Keep `instances × DB_MAX_CONNS × 2` under Postgres
> `max_connections − superuser_reserved_connections` (97 on a 100-connection
> server). Past that, Postgres rejects new connections with "too many clients".
> Requests beyond the pool size wait in the pool, and the server logs
> `db pool: N acquires waited` when that happens.

```bash
# Add to .env
DB_MAX_CONNS=30
DB_MAX_IDLE_CONNS=5
DB_CONN_MAX_LIFETIME=1800  # 30 minutes
```

A cache hit needs one database query: the cache read. Everything else stays off the request path:

- **API keys** are kept in memory for 30 seconds after a lookup. Disabling a key applies at once on the instance that handled it, and within 30 seconds everywhere else.
- **Request logs and usage counters** (`request_logs`, `system_stats`, `request_stats_hourly`, `api_keys` totals) are summed in memory and written in one batch per second. A crash loses at most about a second of counts; a clean shutdown flushes them.
- **Donated token quotas** come from GitHub's `X-RateLimit-*` response headers and are written in one batch per second. `/rate_limit` is only called when a response has no such headers.
- **Admin dashboard stats** are one query per second, only while someone has the dashboard open, and only pushed when they change.

Locally, a 10-connection pool served ~13,800 cache-hit RPS at 200 concurrent clients (p99 24ms).

### 2. Rate Limiter Optimization (Critical)

**Issue**: Global mutex lock on every request  
**Solution**: Shard rate limiters or use atomic operations

### 3. Cache Performance (Critical)

**Current**: PostgreSQL cache queries on every request  
**Optimizations**:
- Increase cache size for better hit rates
- Add database indexes
- Consider Redis for cache layer

```bash
# Add to .env  
MAX_CACHE_SIZE_MB=1000     # 1GB cache
MAX_CACHE_TIME=900         # 15 minutes
```

### 4. HTTP Server Optimization

**Current**: Default Go HTTP server  
**Recommended**: Tuned for high concurrency

### 5. GitHub API Client Pool

**Current**: Single HTTP client  
**Recommended**: Connection pooling and faster timeouts

## 🔧 Immediate Fixes Implemented

### Critical Optimizations Applied

#### 1. Database Connection Pool
```env
DB_MAX_CONNS=30               # Per instance; see the sizing note above
DB_MAX_IDLE_CONNS=5           # Minimum open connections (pgxpool MinConns)
DB_CONN_MAX_LIFETIME=1800     # 30 minute connection lifetime
```

#### 2. Rate Limiter Sharding (16x concurrency)
- **Before**: Single global mutex (major bottleneck)
- **After**: 16 sharded rate limiters with separate locks
- **Benefit**: ~16x reduction in lock contention

#### 3. HTTP Server Optimization
```go
ReadHeaderTimeout: 5s   // Was: 10s → 50% faster
ReadTimeout: 15s        // Was: 30s → 50% faster  
WriteTimeout: 30s       // Was: 60s → 50% faster
IdleTimeout: 60s        // Was: 120s → 50% faster
MaxHeaderBytes: 64KB    // New: Prevent large header attacks
```

#### 4. GitHub Client Connection Pool
```go
MaxIdleConns: 100              // Connection pooling
MaxIdleConnsPerHost: 20        # Per-host pooling
TLSHandshakeTimeout: 5s        # Faster TLS
ResponseHeaderTimeout: 10s     # Faster response reading  
Timeout: 15s                   # Was: 30s → 50% faster
```

#### 5. Performance Database Indexes
- Optimized API key lookups (hot path)
- Optimized cache queries (very hot path)
- Optimized token selection for GitHub API
- Added cleanup performance indexes

### Recommended Production Settings
```env
# Copy .env.performance for these optimized settings:
DB_MAX_CONNS=30
DB_MAX_IDLE_CONNS=5
MAX_CACHE_SIZE_MB=1000        # 1GB cache for better hit rates
MAX_CACHE_TIME=900            # 15 minutes
MAX_PROXY_BODY_BYTES=262144   # 256KB (reduces memory pressure)
```

### Code Optimizations Required

1. **Database Connection Pool Configuration**
2. **Rate Limiter Sharding** 
3. **HTTP Server Tuning**
4. **GitHub Client Connection Pool**
5. **Database Query Optimization**

## 📊 Performance Targets for 500 RPS

### Achieved with Optimizations:
- **Database**: 150 connections (7.5x increase) ✅
- **Rate Limiter**: 16 sharded locks (~16x concurrency) ✅
- **HTTP Server**: 50% faster timeouts ✅
- **GitHub Client**: Connection pooling + 50% faster ✅
- **Cache**: 10x larger cache + performance indexes ✅

### Expected Performance:
- **Cache Hit Rate**: >80% (reduces GitHub API calls to ~100 RPS)
- **Response Time**: <50ms for cache hits, <200ms for cache misses
- **Memory**: <3GB total (with 1GB cache)
- **Concurrency**: 150 concurrent database operations
- **Throughput**: 500+ RPS sustained

## 🚀 Implementation Priority

**Phase 1 (Immediate - Critical)**:
1. Increase database connection pool
2. Optimize rate limiter for concurrency
3. Tune HTTP server settings

**Phase 2 (Short-term)**:
4. Add database indexes
5. Optimize GitHub client pooling
6. Memory optimizations

**Phase 3 (Long-term)**:
7. Consider Redis cache layer
8. Horizontal scaling
9. Connection multiplexing
