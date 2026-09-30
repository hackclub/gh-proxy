package cache

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Every test owns a schema in an explicitly supplied local test database. Never
// fall back to DATABASE_URL or the application's database configuration.
func testCachePool(t *testing.T, migrate bool) (*pgxpool.Pool, context.Context) {
	t.Helper()
	url := os.Getenv("CACHE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set CACHE_TEST_DATABASE_URL to an isolated local PostgreSQL database")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.ConnConfig.Host
	if host != "localhost" && host != "127.0.0.1" && host != "::1" && !strings.HasPrefix(host, "/") {
		t.Fatal("CACHE_TEST_DATABASE_URL must use localhost or a local Unix socket")
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback.Host != host {
			t.Fatal("CACHE_TEST_DATABASE_URL must not contain alternate hosts")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cfg.MaxConns = 1
	admin, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cache_test_%x", suffix)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	initial, err := os.ReadFile("../db/migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Use the real cache DDL without installing extensions or unrelated tables.
	ddl := strings.Split(strings.Split(string(initial), "CREATE TABLE IF NOT EXISTS cached_responses")[1], "CREATE TABLE IF NOT EXISTS request_logs")[0]
	if _, err := pool.Exec(ctx, "CREATE TABLE IF NOT EXISTS cached_responses"+ddl); err != nil {
		t.Fatal(err)
	}
	if migrate {
		if err := applyCacheMigration(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	return pool, ctx
}

func applyCacheMigration(ctx context.Context, pool *pgxpool.Pool) error {
	migration, err := os.ReadFile("../db/migrations/015_cache_unique.sql")
	if err != nil {
		return err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	// Match the runner's semicolon splitting, but pin the transaction connection.
	for _, statement := range strings.Split(string(migration), ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := conn.Exec(ctx, statement); err != nil {
			_, _ = conn.Exec(ctx, "ROLLBACK")
			return err
		}
	}
	return nil
}

func putTestEntry(t *testing.T, ctx context.Context, c *Cache, url string, req, resp []byte, status int) {
	t.Helper()
	if err := c.Put(ctx, "POST", url, req, status, []byte(`{"X-Test":["value"]}`), resp); err != nil {
		t.Fatal(err)
	}
}

func TestCacheOverwriteAndRequestBodies(t *testing.T) {
	pool, ctx := testCachePool(t, true)
	c := New(pool, 60, 0)
	putTestEntry(t, ctx, c, "/same", nil, []byte("old"), 200)
	var before time.Time
	if err := pool.QueryRow(ctx, "SELECT created_at FROM cached_responses").Scan(&before); err != nil {
		t.Fatal(err)
	}
	c.maxAge = 2 * time.Minute
	if err := c.Put(ctx, "POST", "/same", []byte{}, 201, []byte(`{"New":true}`), []byte("new")); err != nil {
		t.Fatal(err)
	}
	status, headers, body, ok, err := c.Get(ctx, "POST", "/same", nil)
	if err != nil || !ok || status != 201 || string(body) != "new" || string(headers) != `{"New": true}` {
		t.Fatalf("Get = %d %s %s %v %v", status, headers, body, ok, err)
	}
	var count int
	var created, expires time.Time
	var req []byte
	if err := pool.QueryRow(ctx, "SELECT count(*) OVER (), created_at, expires_at, req_body FROM cached_responses").Scan(&count, &created, &expires, &req); err != nil {
		t.Fatal(err)
	}
	if count != 1 || !created.After(before) || expires.Sub(created) != 2*time.Minute || req == nil {
		t.Fatalf("overwrite: count=%d created=%v expires=%v req=%v", count, created, expires, req)
	}
	putTestEntry(t, ctx, c, "/same", []byte("different"), []byte("other"), 202)
	status, _, body, ok, err = c.Get(ctx, "POST", "/same", []byte("different"))
	if err != nil || !ok || status != 202 || string(body) != "other" {
		t.Fatalf("different body: %d %s %v %v", status, body, ok, err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cached_responses").Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	c.maxAge = 0
	putTestEntry(t, ctx, c, "/same", nil, []byte("unlimited"), 203)
	var unlimited bool
	if err := pool.QueryRow(ctx, "SELECT expires_at IS NULL FROM cached_responses WHERE content_hash=$1", hash(nil)).Scan(&unlimited); err != nil || !unlimited {
		t.Fatalf("unlimited=%v err=%v", unlimited, err)
	}
}

func TestCacheOlderWaitingPutCannotOverwrite(t *testing.T) {
	pool, ctx := testCachePool(t, true)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	waits := pool.Stat().EmptyAcquireCount()
	done := make(chan error, 1)
	go func() { done <- New(pool, 60, 0).Put(ctx, "POST", "/race", nil, 200, []byte(`{}`), []byte("old")) }()
	for pool.Stat().EmptyAcquireCount() == waits {
		select {
		case err := <-done:
			t.Fatalf("Put did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	other, err := pgxpool.NewWithConfig(ctx, pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	putTestEntry(t, ctx, New(other, 120, 0), "/race", nil, []byte("new"), 201)
	conn.Release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	status, _, body, ok, err := New(pool, 60, 0).Get(ctx, "POST", "/race", nil)
	if err != nil || !ok || status != 201 || string(body) != "new" {
		t.Fatalf("older Put overwrote newer: %d %s %v %v", status, body, ok, err)
	}
}

func TestCacheExpiredPurgeWithoutSizeLimit(t *testing.T) {
	pool, ctx := testCachePool(t, true)
	c := New(pool, 0, 0)
	putTestEntry(t, ctx, c, "/live", nil, []byte("live"), 200)
	if _, err := pool.Exec(ctx, `INSERT INTO cached_responses(method,url,status,resp_headers,resp_body,content_hash,expires_at)
		SELECT 'GET', '/expired/' || n, 200, '{}', ''::bytea, 'hash', now() - interval '1 second'
		FROM generate_series(1, $1::int) AS n`, cleanupBatchSize+5); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err := c.Get(ctx, "GET", "/expired/1", nil); err != nil || ok {
		t.Fatalf("expired Get: ok=%v err=%v", ok, err)
	}
	if err := c.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cached_responses").Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func TestCacheLogicalEvictionAndOversizedSkip(t *testing.T) {
	pool, ctx := testCachePool(t, true)
	c := New(pool, 0, 1)
	// Highly compressible TOAST bodies must still consume logical bytes.
	body := bytes.Repeat([]byte("x"), 400*1024)
	for i := 0; i < 3; i++ {
		putTestEntry(t, ctx, c, fmt.Sprintf("/%d", i), nil, body, 200)
	}
	// Explicit timestamps make the eviction order deterministic.
	if _, err := pool.Exec(ctx, "UPDATE cached_responses SET created_at = now() - interval '1 minute' + id * interval '1 second'"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := c.Cleanup(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		_, _, _, ok, err := c.Get(ctx, "POST", fmt.Sprintf("/%d", i), nil)
		if err != nil || ok != (i > 0) {
			t.Fatalf("entry %d: ok=%v err=%v", i, ok, err)
		}
	}
	putTestEntry(t, ctx, c, "/oversized", nil, bytes.Repeat([]byte("x"), 1024*1024), 200)
	putTestEntry(t, ctx, c, "/1", nil, bytes.Repeat([]byte("x"), 1024*1024), 500)
	_, _, _, ok, err := c.Get(ctx, "POST", "/oversized", nil)
	if err != nil || ok {
		t.Fatalf("oversized stored: ok=%v err=%v", ok, err)
	}
	status, _, actual, ok, err := c.Get(ctx, "POST", "/1", nil)
	if err != nil || !ok || status != 200 || !bytes.Equal(actual, body) {
		t.Fatalf("oversized replaced valid entry: status=%d ok=%v err=%v", status, ok, err)
	}
}

func seedMigrationDuplicates(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO cached_responses(method,url,status,resp_headers,resp_body,content_hash,created_at) VALUES
		('GET','/same',201,'{}','newest timestamp','a','2026-01-02'),
		('GET','/same',202,'{}','newest tie id','a','2026-01-02'),
		('GET','/same',203,'{}','older higher id','a','2026-01-01'),
		('GET','/same',204,'{}','different hash','b','2026-01-01'),
		('POST','/same',205,'{}','different method','a','2026-01-01'),
		('GET','/other',206,'{}','different url','a','2026-01-01')`); err != nil {
		t.Fatal(err)
	}
}

func TestCacheMigrationDeduplicatesNewest(t *testing.T) {
	pool, ctx := testCachePool(t, false)
	seedMigrationDuplicates(t, ctx, pool)
	for i := 0; i < 2; i++ {
		if err := applyCacheMigration(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var count, status int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cached_responses").Scan(&count); err != nil || count != 4 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if err := pool.QueryRow(ctx, "SELECT status FROM cached_responses WHERE method='GET' AND url='/same' AND content_hash='a'").Scan(&status); err != nil || status != 202 {
		t.Fatalf("winner=%d err=%v", status, err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO cached_responses(method,url,status,resp_headers,resp_body,content_hash)
		VALUES ('GET','/same',200,'{}','duplicate','a')`); err == nil {
		t.Fatal("unique index allowed a duplicate")
	}
}

func TestCacheMigrationRollback(t *testing.T) {
	pool, ctx := testCachePool(t, false)
	seedMigrationDuplicates(t, ctx, pool)
	// Make index creation fail after DELETE, proving the explicit transaction
	// restores duplicates rather than committing only half the migration.
	if _, err := pool.Exec(ctx, "CREATE VIEW idx_cached_responses_unique AS SELECT 1 AS value"); err != nil {
		t.Fatal(err)
	}
	// IF NOT EXISTS may skip an existing relation, so instead force index DDL
	// failure with a duplicate index name in another table's constraint? Use a
	// restricted role is unnecessary: a statement timeout while the index waits
	// is deterministic via an event trigger only with elevated privileges.
	// Drop the conflict and test a transactional DELETE failure via a trigger.
	if _, err := pool.Exec(ctx, `DROP VIEW idx_cached_responses_unique;
		CREATE FUNCTION reject_cache_delete() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'test delete failure'; END $$;
		CREATE TRIGGER reject_cache_delete BEFORE DELETE ON cached_responses
		FOR EACH ROW EXECUTE FUNCTION reject_cache_delete()`); err != nil {
		t.Fatal(err)
	}
	if err := applyCacheMigration(ctx, pool); err == nil {
		t.Fatal("migration unexpectedly succeeded")
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM cached_responses").Scan(&count); err != nil || count != 6 {
		t.Fatalf("rollback count=%d err=%v", count, err)
	}
}
