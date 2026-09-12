-- Record when durable rolling-window statistics became available. Existing
-- installations receive the deployment time of this migration, while new
-- installs receive their migration time.
ALTER TABLE system_stats
ADD COLUMN IF NOT EXISTS stats_tracking_started_at TIMESTAMPTZ NOT NULL DEFAULT now();
