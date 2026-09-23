-- Sum of proxied request wall times per hour, for average response time.
-- Rows from before this migration have no samples, so averages divide by
-- latency_samples rather than requests.
ALTER TABLE request_stats_hourly ADD COLUMN IF NOT EXISTS latency_us_sum BIGINT NOT NULL DEFAULT 0;
ALTER TABLE request_stats_hourly ADD COLUMN IF NOT EXISTS latency_samples BIGINT NOT NULL DEFAULT 0;
