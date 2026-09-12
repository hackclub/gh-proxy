-- Keep durable hourly request counts for rolling homepage statistics.
CREATE TABLE IF NOT EXISTS request_stats_hourly (
    hour TIMESTAMPTZ PRIMARY KEY,
    requests BIGINT NOT NULL DEFAULT 0
);

-- Preserve any recent history still available when this migration runs.
INSERT INTO request_stats_hourly (hour, requests)
SELECT date_trunc('hour', created_at), COUNT(*)
FROM request_logs
GROUP BY 1
ON CONFLICT (hour) DO NOTHING;
