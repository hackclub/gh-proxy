BEGIN;

-- Block writers until deduplication and the unique index are both committed
LOCK TABLE cached_responses IN SHARE ROW EXCLUSIVE MODE;

-- Keep the newest timestamp, breaking ties with the largest id
DELETE FROM cached_responses
WHERE id IN (
  SELECT id FROM (
    SELECT id, row_number() OVER (
      PARTITION BY method, url, content_hash
      ORDER BY created_at DESC, id DESC
    ) AS position
    FROM cached_responses
  ) AS ranked
  WHERE position > 1
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_cached_responses_unique
ON cached_responses(method, url, content_hash);

COMMIT;
