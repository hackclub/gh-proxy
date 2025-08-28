-- EMERGENCY FIX for production migration 009 failure
-- Run this manually in production database to fix the migration state

-- Create the system_stats table
CREATE TABLE IF NOT EXISTS system_stats (
    id INT PRIMARY KEY DEFAULT 1,
    total_requests BIGINT NOT NULL DEFAULT 0,
    total_cached_requests BIGINT NOT NULL DEFAULT 0,
    today_requests BIGINT NOT NULL DEFAULT 0,
    today_date DATE NOT NULL DEFAULT CURRENT_DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Add the columns to api_keys
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_requests BIGINT NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS total_cached_requests BIGINT NOT NULL DEFAULT 0;

-- Initialize the system_stats row
INSERT INTO system_stats (id) VALUES (1) ON CONFLICT DO NOTHING;

-- Mark migration 009 as completed so server can start
INSERT INTO schema_migrations (name) VALUES ('009_cumulative_stats.sql') ON CONFLICT DO NOTHING;
