package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Cache struct {
	pool *pgxpool.Pool
	maxAge time.Duration
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
		if err == sql.ErrNoRows { return 0, nil, nil, false, nil }
		return
	}
	return status, headers, resp, true, nil
}

func (c *Cache) Put(ctx context.Context, method, url string, reqBody []byte, status int, respHeaders []byte, respBody []byte) error {
	var expires *time.Time
	if c.maxAge > 0 { t := time.Now().Add(c.maxAge); expires = &t } // 0 => unlimited (NULL)
	_, err := c.pool.Exec(ctx, `INSERT INTO cached_responses(method,url,req_body,status,resp_headers,resp_body,expires_at,content_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, method, url, reqBody, status, respHeaders, respBody, expires, hash(reqBody))
	return err
}

func (c *Cache) Cleanup(ctx context.Context) error {
	// enforce size limit in MB by trimming oldest rows
	if c.maxSizeMB <= 0 { return nil }
	// approximate table size using pg_total_relation_size
	var bytes int64
	if err := c.pool.QueryRow(ctx, `SELECT COALESCE(pg_total_relation_size('cached_responses')::bigint,0)`).Scan(&bytes); err != nil { return err }
	limit := c.maxSizeMB * 1024 * 1024
	if bytes <= limit { return nil }
	// delete oldest 10% and recheck next time
	cmdTag, err := c.pool.Exec(ctx, `DELETE FROM cached_responses WHERE id IN (SELECT id FROM cached_responses ORDER BY created_at ASC LIMIT (SELECT GREATEST(1, (SELECT count(*) FROM cached_responses)/10)))`)
	if err == nil {
		log.Printf("cache: trimmed %d rows (table ~%d MB)", cmdTag.RowsAffected(), bytes/1024/1024)
	}
	return err
}
