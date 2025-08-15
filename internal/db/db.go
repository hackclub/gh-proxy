package db

import (
	"context"
	"embed"
	"fmt"
	"strings"
	"log"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil { return nil, err }
	// allow tuning via env DB_MAX_CONNS; pgxpool supports MaxConns on config
	if maxStr := os.Getenv("DB_MAX_CONNS"); maxStr != "" {
		if v, err := strconv.ParseInt(maxStr, 10, 32); err == nil && v > 0 {
			cfg.MaxConns = int32(v)
		}
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// very simple migration runner: apply files in lexical order, idempotently
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil { return err }
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text primary key)`); err != nil { return err }
	for _, e := range entries {
		name := e.Name()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&exists); err != nil { return err }
		if exists { continue }
		b, err := migrationsFS.ReadFile("migrations/"+name)
		if err != nil { return err }
		sql := string(b)
		// split on ; but naive — keep simple migrations
		stmts := strings.Split(sql, ";")
		batch := &pgx.Batch{}
		for _, s := range stmts {
			s = strings.TrimSpace(s)
			if s == "" { continue }
			batch.Queue(s)
		}
		br := pool.SendBatch(ctx, batch)
		if err := br.Close(); err != nil { return fmt.Errorf("migration %s: %w", name, err) }
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil { return err }
		log.Printf("migration applied: %s", name)
	}
	return nil
}
