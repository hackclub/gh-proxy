-- Performance indexes for high throughput

-- Optimize API key lookups (hot path)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_api_keys_key_hash_active 
ON api_keys(key_hash) WHERE disabled = false;

-- Optimize cache lookups (very hot path)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cached_responses_lookup 
ON cached_responses(method, url, content_hash, expires_at) 
WHERE expires_at IS NULL OR expires_at > now();

-- Optimize token selection for GitHub API
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_donated_tokens_active_last_ok 
ON donated_tokens(last_ok_at, id) WHERE revoked = false;

-- Optimize rate limit lookups
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_token_rate_limits_lookup 
ON token_rate_limits(token_id, category, remaining, reset);

-- Optimize request log queries (admin dashboard)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_request_logs_recent 
ON request_logs(created_at DESC, api_key) WHERE created_at > now() - interval '7 days';

-- Optimize cache cleanup (janitor performance)
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_cached_responses_cleanup 
ON cached_responses(created_at) WHERE expires_at < now();
