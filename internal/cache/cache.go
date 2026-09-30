package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Cache struct {
	pool      *pgxpool.Pool
	maxAge    time.Duration
	maxSizeMB int64
}

func New(pool *pgxpool.Pool, maxAgeSeconds int64, maxSizeMB int64) *Cache {
	age := time.Duration(maxAgeSeconds) * time.Second
	return &Cache{pool: pool, maxAge: age, maxSizeMB: maxSizeMB}
}

func hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c *Cache) Get(ctx context.Context, method, url string, body []byte) (status int, headers []byte, resp []byte, ok bool, err error) {
	contentHash := hash(body)
	row := c.pool.QueryRow(ctx, `SELECT status, resp_headers, resp_body FROM cached_responses WHERE method=$1 AND url=$2 AND content_hash=$3 AND (expires_at IS NULL OR expires_at > now()) ORDER BY id DESC LIMIT 1`, method, url, contentHash)
	err = row.Scan(&status, &headers, &resp)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, nil, nil, false, nil
		}
		return
	}
	return status, headers, resp, true, nil
}

// Estimate live row bytes, not allocated heap/index space. Keep bodies out of
// the composite: octet_length(bytea) reads TOAST length metadata without fetching
// the payload, and counts compressible bodies against the budget too.
const logicalRowSize = `pg_column_size(ROW(entry.id, entry.method, entry.url,
	entry.status, entry.content_hash, entry.created_at, entry.expires_at))::bigint
	+ COALESCE(GREATEST(pg_column_size(entry.req_body)::bigint, octet_length(entry.req_body)::bigint), 0)
	+ pg_column_size(entry.resp_headers)::bigint
	+ GREATEST(pg_column_size(entry.resp_body)::bigint, octet_length(entry.resp_body)::bigint)`

func (c *Cache) Put(ctx context.Context, method, url string, reqBody []byte, status int, respHeaders []byte, respBody []byte) error {
	// Capture before waiting for a connection or executing SQL. A delayed Put
	// must not replace a write from a later Put (not upstream fetch start time).
	created := time.Now().UTC().Truncate(time.Microsecond)
	var expires *time.Time
	if c.maxAge > 0 {
		t := created.Add(c.maxAge)
		expires = &t
	} // 0 => unlimited (NULL)
	_, err := c.pool.Exec(ctx, `WITH entry AS (
		SELECT 0::bigint AS id, $1::text AS method, $2::text AS url,
			$3::bytea AS req_body, $4::int AS status, $5::jsonb AS resp_headers,
			$6::bytea AS resp_body, $7::timestamptz AS expires_at,
			$8::text AS content_hash, $9::timestamptz AS created_at
	)
	INSERT INTO cached_responses(method,url,req_body,status,resp_headers,resp_body,expires_at,content_hash,created_at)
	SELECT method,url,req_body,status,resp_headers,resp_body,expires_at,content_hash,created_at
	FROM entry WHERE $10::bigint <= 0 OR (`+logicalRowSize+`) <= $10::bigint
	ON CONFLICT (method,url,content_hash) DO UPDATE SET
		req_body = EXCLUDED.req_body, status = EXCLUDED.status,
		resp_headers = EXCLUDED.resp_headers, resp_body = EXCLUDED.resp_body,
		created_at = EXCLUDED.created_at, expires_at = EXCLUDED.expires_at
	WHERE cached_responses.created_at < EXCLUDED.created_at`,
		method, url, reqBody, status, respHeaders, respBody, expires, hash(reqBody), created, c.maxSizeMB*1024*1024)
	return err
}

const cleanupBatchSize = 500

func (c *Cache) Cleanup(ctx context.Context) error {
	// Expiry is independent of the size budget. Each statement deletes at most
	// one batch, releasing locks between batches and respecting cancellation.
	if err := c.deleteBatches(ctx, "expired", `DELETE FROM cached_responses
		WHERE id IN (SELECT id FROM cached_responses WHERE expires_at <= now()
			ORDER BY expires_at, id LIMIT $1) AND expires_at <= now()`, cleanupBatchSize); err != nil {
		return err
	}
	if c.maxSizeMB <= 0 {
		return nil
	}
	// Retain the newest prefix that fits the logical budget, evicting oldest
	// first. Recheck created_at so a concurrent refresh isn't deleted by an
	// eviction decision made using its previous contents.
	return c.deleteBatches(ctx, "evicted", `WITH ranked AS (
		SELECT entry.id, entry.created_at,
			SUM(`+logicalRowSize+`) OVER (ORDER BY entry.created_at DESC, entry.id DESC
				ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS running_bytes
		FROM cached_responses AS entry WHERE expires_at IS NULL OR expires_at > now()
	), victims AS (
		SELECT id, created_at FROM ranked WHERE running_bytes > $1
		ORDER BY created_at, id LIMIT $2
	)
	DELETE FROM cached_responses AS entry USING victims
	WHERE entry.id = victims.id AND entry.created_at = victims.created_at`,
		c.maxSizeMB*1024*1024, cleanupBatchSize)
}

func (c *Cache) deleteBatches(ctx context.Context, action, query string, args ...any) error {
	for {
		tag, err := c.pool.Exec(ctx, query, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		log.Printf("cache: %s %d rows", action, tag.RowsAffected())
	}
}
