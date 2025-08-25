-- Performance indexes for high throughput
-- Note: Removed CONCURRENTLY to work within migration transactions

-- Optimize API key lookups (hot path)
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash_active 
ON api_keys(key_hash) WHERE disabled = false;

-- Optimize cache lookups (very hot path)
CREATE INDEX IF NOT EXISTS idx_cached_responses_lookup 
ON cached_responses(method, url, content_hash);

-- Optimize token selection for GitHub API
CREATE INDEX IF NOT EXISTS idx_donated_tokens_active_last_ok 
ON donated_tokens(last_ok_at, id) WHERE revoked = false;

-- Optimize rate limit lookups
CREATE INDEX IF NOT EXISTS idx_token_rate_limits_lookup 
ON token_rate_limits(token_id, category);

-- Optimize request log queries (admin dashboard)
CREATE INDEX IF NOT EXISTS idx_request_logs_recent 
ON request_logs(created_at DESC, api_key);

-- Optimize cache cleanup (janitor performance)
CREATE INDEX IF NOT EXISTS idx_cached_responses_cleanup 
ON cached_responses(created_at);
