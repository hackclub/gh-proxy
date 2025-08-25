package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"gh-proxy/internal/config"
)

func main() {
	log.Println("🔄 Starting token migration from dev to prod...")

	cfg := config.Load()

	// Get production database URL from environment
	prodDBURL := os.Getenv("PROD_DB_URL")
	if prodDBURL == "" {
		log.Fatal("❌ PROD_DB_URL environment variable not set")
	}

	// Connect to dev database (current DATABASE_URL)
	log.Printf("📡 Connecting to dev database...")
	devPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to dev database: %v", err)
	}
	defer devPool.Close()

	// Connect to production database
	log.Printf("📡 Connecting to prod database...")
	prodPool, err := pgxpool.New(context.Background(), prodDBURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to prod database: %v", err)
	}
	defer prodPool.Close()

	// Test connections
	if err := devPool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ Dev database ping failed: %v", err)
	}
	if err := prodPool.Ping(context.Background()); err != nil {
		log.Fatalf("❌ Prod database ping failed: %v", err)
	}
	log.Println("✅ Database connections successful")

	ctx := context.Background()

	// Count tokens to migrate (excluding zachlatta)
	var devCount int
	err = devPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM donated_tokens 
		WHERE github_user != 'zachlatta' AND revoked = false
	`).Scan(&devCount)
	if err != nil {
		log.Fatalf("❌ Failed to count dev tokens: %v", err)
	}

	log.Printf("📊 Found %d non-zachlatta active tokens in dev database", devCount)

	if devCount == 0 {
		log.Println("ℹ️  No tokens to migrate")
		return
	}

	// Check if prod table exists and count existing tokens
	var prodCount int
	err = prodPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM donated_tokens 
		WHERE github_user != 'zachlatta'
	`).Scan(&prodCount)
	if err != nil {
		log.Fatalf("❌ Failed to count prod tokens (make sure migrations are run on prod): %v", err)
	}

	log.Printf("📊 Current prod database has %d non-zachlatta tokens", prodCount)

	// Fetch tokens from dev database (excluding zachlatta)
	rows, err := devPool.Query(ctx, `
		SELECT id, github_user, token, created_at, revoked, last_ok_at, scopes
		FROM donated_tokens 
		WHERE github_user != 'zachlatta' AND revoked = false
		ORDER BY created_at ASC
	`)
	if err != nil {
		log.Fatalf("❌ Failed to fetch dev tokens: %v", err)
	}
	defer rows.Close()

	var migrated, skipped int

	// Begin transaction for production inserts
	tx, err := prodPool.Begin(ctx)
	if err != nil {
		log.Fatalf("❌ Failed to begin prod transaction: %v", err)
	}
	defer tx.Rollback(ctx) // Will be no-op if committed

	log.Println("🔄 Starting token migration...")

	for rows.Next() {
		var id, githubUser, token, scopes string
		var createdAt, lastOkAt interface{}
		var revoked bool

		err := rows.Scan(&id, &githubUser, &token, &createdAt, &revoked, &lastOkAt, &scopes)
		if err != nil {
			log.Printf("❌ Failed to scan row: %v", err)
			continue
		}

		// Try to insert into production database
		// Use ON CONFLICT to handle duplicates gracefully
		_, err = tx.Exec(ctx, `
			INSERT INTO donated_tokens (id, github_user, token, created_at, revoked, last_ok_at, scopes)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (github_user) DO UPDATE SET
				token = EXCLUDED.token,
				last_ok_at = EXCLUDED.last_ok_at,
				scopes = EXCLUDED.scopes,
				revoked = EXCLUDED.revoked
		`, id, githubUser, token, createdAt, revoked, lastOkAt, scopes)

		if err != nil {
			log.Printf("⚠️  Failed to insert token for %s: %v", githubUser, err)
			skipped++
		} else {
			log.Printf("✅ Migrated token for @%s", githubUser)
			migrated++
		}
	}

	if err = rows.Err(); err != nil {
		log.Fatalf("❌ Error reading rows: %v", err)
	}

	// Commit transaction
	if err = tx.Commit(ctx); err != nil {
		log.Fatalf("❌ Failed to commit transaction: %v", err)
	}

	log.Println("")
	log.Printf("🎉 Migration completed!")
	log.Printf("✅ Successfully migrated: %d tokens", migrated)
	if skipped > 0 {
		log.Printf("⚠️  Skipped (errors): %d tokens", skipped)
	}

	// Verify final count
	var finalCount int
	err = prodPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM donated_tokens 
		WHERE github_user != 'zachlatta'
	`).Scan(&finalCount)
	if err != nil {
		log.Printf("⚠️  Failed to verify final count: %v", err)
	} else {
		log.Printf("📊 Final prod database count: %d non-zachlatta tokens", finalCount)
	}

	log.Println("🚀 Token migration complete!")
}
