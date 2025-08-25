package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"gh-proxy/internal/config"
)

func main() {
	log.Println("🔍 Verifying token migration...")

	cfg := config.Load()

	// Get production database URL from environment
	prodDBURL := os.Getenv("PROD_DB_URL")
	if prodDBURL == "" {
		log.Fatal("❌ PROD_DB_URL environment variable not set")
	}

	// Connect to dev database
	devPool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to dev database: %v", err)
	}
	defer devPool.Close()

	// Connect to production database
	prodPool, err := pgxpool.New(context.Background(), prodDBURL)
	if err != nil {
		log.Fatalf("❌ Failed to connect to prod database: %v", err)
	}
	defer prodPool.Close()

	ctx := context.Background()

	// Count tokens in dev
	var devCount int
	err = devPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM donated_tokens 
		WHERE github_user != 'zachlatta' AND revoked = false
	`).Scan(&devCount)
	if err != nil {
		log.Fatalf("❌ Failed to count dev tokens: %v", err)
	}

	// Count tokens in prod
	var prodCount int
	err = prodPool.QueryRow(ctx, `
		SELECT COUNT(*) FROM donated_tokens 
		WHERE github_user != 'zachlatta'
	`).Scan(&prodCount)
	if err != nil {
		log.Fatalf("❌ Failed to count prod tokens: %v", err)
	}

	log.Printf("📊 Dev database: %d active non-zachlatta tokens", devCount)
	log.Printf("📊 Prod database: %d non-zachlatta tokens", prodCount)

	if prodCount >= devCount {
		log.Printf("✅ Migration appears successful! Prod has %d tokens (>= dev's %d)", prodCount, devCount)
	} else {
		log.Printf("⚠️  Migration may be incomplete. Prod has %d tokens (< dev's %d)", prodCount, devCount)
	}

	// Show some sample users in prod
	log.Println("\n📋 Sample migrated users in production:")
	rows, err := prodPool.Query(ctx, `
		SELECT github_user, created_at::date 
		FROM donated_tokens 
		WHERE github_user != 'zachlatta' 
		ORDER BY created_at DESC 
		LIMIT 10
	`)
	if err != nil {
		log.Printf("⚠️  Failed to fetch sample users: %v", err)
		return
	}
	defer rows.Close()

	for rows.Next() {
		var user string
		var date string
		if err := rows.Scan(&user, &date); err != nil {
			continue
		}
		log.Printf("  • @%s (created: %s)", user, date)
	}

	log.Println("\n🎉 Verification complete!")
}
