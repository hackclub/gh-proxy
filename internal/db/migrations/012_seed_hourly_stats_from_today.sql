-- Migration 011 could only backfill the 1,000 request rows retained by the
-- logs janitor. The durable today_requests counter is a better baseline for
-- the current day. Replace today's partial hourly backfill with that count.
DELETE FROM request_stats_hourly
WHERE (hour AT TIME ZONE 'America/New_York')::date =
      (now() AT TIME ZONE 'America/New_York')::date;

INSERT INTO request_stats_hourly (hour, requests)
SELECT date_trunc('hour', now()), today_requests
FROM system_stats
WHERE id = 1
  AND today_date = (now() AT TIME ZONE 'America/New_York')::date
ON CONFLICT (hour) DO UPDATE SET requests = EXCLUDED.requests;
