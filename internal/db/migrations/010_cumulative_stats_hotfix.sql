-- HOTFIX: Ensure system_stats table exists and is properly initialized
-- This fixes production deployment issues with migration 009

-- Create system_stats table (idempotent)
CREATE TABLE IF NOT EXISTS system_stats (
    id INT PRIMARY KEY DEFAULT 1,
    total_requests BIGINT NOT NULL DEFAULT 0,
    total_cached_requests BIGINT NOT NULL DEFAULT 0,
    today_requests BIGINT NOT NULL DEFAULT 0,
    today_date DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Add api_keys columns (idempotent)
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_cached_requests BIGINT NOT NULL DEFAULT 0;

-- Initialize system_stats row (safe upsert)
INSERT INTO system_stats (id, total_requests, total_cached_requests, today_requests, today_date) 
VALUES (1, 0, 0, 0, CURRENT_DATE) 
ON CONFLICT (id) DO NOTHING;
