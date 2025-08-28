-- Add cumulative stats tracking for accurate metrics
-- This migration addresses the issue where stats were calculated from truncated request_logs

-- System-wide cumulative stats
CREATE TABLE IF NOT EXISTS system_stats (
    id INT PRIMARY KEY DEFAULT 1,
    total_requests BIGINT NOT NULL DEFAULT 0,
    total_cached_requests BIGINT NOT NULL DEFAULT 0,
    today_requests BIGINT NOT NULL DEFAULT 0,
    today_date DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Add cumulative stats to api_keys for per-key tracking  
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_requests BIGINT NOT NULL DEFAULT 0;

ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_cached_requests BIGINT NOT NULL DEFAULT 0;

-- Ensure only one row exists in system_stats
INSERT INTO system_stats (id) VALUES (1) ON CONFLICT DO NOTHING;
