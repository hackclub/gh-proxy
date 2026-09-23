package db

import (
	"context"
	"embed"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"gh-proxy/internal/config"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

func Connect(ctx context.Context, appCfg config.Config) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(appCfg.DatabaseURL)
	if err != nil {
		return nil, err
	}

	if appCfg.DBMaxConns < 1 {
		return nil, fmt.Errorf("DB_MAX_CONNS must be at least 1, got %d", appCfg.DBMaxConns)
	}
	cfg.MaxConns = appCfg.DBMaxConns
	cfg.MinConns = min(max(appCfg.DBMaxIdleConns, 0), cfg.MaxConns)
	cfg.MaxConnLifetime = time.Duration(appCfg.DBConnMaxLifetime) * time.Second
	cfg.MaxConnLifetimeJitter = cfg.MaxConnLifetime / 10
	cfg.MaxConnIdleTime = 5 * time.Minute
	cfg.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	checkServerCapacity(ctx, pool)
	return pool, nil
}

func checkServerCapacity(ctx context.Context, pool *pgxpool.Pool) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var maxConns, reserved, inUse int32
	err := pool.QueryRow(ctx, `
		SELECT current_setting('max_connections')::int,
		       current_setting('superuser_reserved_connections')::int,
		       (SELECT count(*) FROM pg_stat_activity WHERE backend_type = 'client backend')::int`).Scan(&maxConns, &reserved, &inUse)
	if err != nil {
		log.Printf("db: could not read server connection limits: %v", err)
		return
	}
	available := maxConns - reserved
	poolMax := pool.Config().MaxConns
	log.Printf("db: pool max=%d min=%d; server allows %d client connections (%d in use)", poolMax, pool.Config().MinConns, available, inUse)
	if poolMax > available/2 {
		log.Printf("db: WARNING pool max (%d) is more than half of the server's %d usable connections; a rolling deploy or second replica can exhaust them. Lower DB_MAX_CONNS.", poolMax, available)
	}
}

// MonitorPool logs pool pressure: requests that had to wait for a free
// connection, and cumulative wait time. Quiet when the pool is healthy.
func MonitorPool(ctx context.Context, pool *pgxpool.Pool, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	prev := pool.Stat()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := pool.Stat()
		waits := st.EmptyAcquireCount() - prev.EmptyAcquireCount()
		waited := st.EmptyAcquireWaitTime() - prev.EmptyAcquireWaitTime()
		canceled := st.CanceledAcquireCount() - prev.CanceledAcquireCount()
		if waits > 0 || canceled > 0 {
			log.Printf("db pool: %d acquires waited (%s total), %d canceled; in use %d/%d, idle %d",
				waits, waited.Round(time.Millisecond), canceled, st.AcquiredConns(), st.MaxConns(), st.IdleConns())
		}
		prev = st
	}
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	// very simple migration runner: apply files in lexical order, idempotently
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (name text primary key)`); err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE name=$1)`, name).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		sql := string(b)
		// split on ; but naive — keep simple migrations.
		// Execute statement-by-statement: pgx preprocesses a Batch in full before
		// running any statement, so a CREATE TABLE followed by an INSERT in the
		// same file would fail at prepare time ("relation does not exist").
		stmts := strings.Split(sql, ";")
		for _, s := range stmts {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			if _, err := pool.Exec(ctx, s); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); err != nil {
			return err
		}
		log.Printf("migration applied: %s", name)
	}
	return nil
}
