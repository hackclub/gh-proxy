package config

import (
	"log"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL       string
	BaseURL           string
	AdminUser         string
	AdminPass         string
	GithubClientID    string
	GithubClientSecret string
	MaxCacheTime      timeDuration
	MaxCacheSizeMB    int64
	DBMaxConns        int32
	MaxProxyBodyBytes int64
}

type timeDuration struct{ Seconds int64 }

func (d timeDuration) Duration() int64 { return d.Seconds }

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" { return def }
	return v
}

func Load() Config {
	_ = loadDotenv()
	maxCacheTime := parseInt(getenv("MAX_CACHE_TIME", "300"))
	maxCacheSize := parseInt(getenv("MAX_CACHE_SIZE_MB", "100"))
	cfg := Config{
		DatabaseURL:        getenv("DATABASE_URL", "postgres://ghproxy:ghproxy@localhost:5433/ghproxy?sslmode=disable"),
		BaseURL:            getenv("BASE_URL", "http://localhost:8080"),
		AdminUser:          getenv("ADMIN_USER", "admin"),
		AdminPass:          getenv("ADMIN_PASS", "admin"),
		GithubClientID:     os.Getenv("GITHUB_OAUTH_CLIENT_ID"),
		GithubClientSecret: os.Getenv("GITHUB_OAUTH_CLIENT_SECRET"),
		MaxCacheTime:       timeDuration{Seconds: maxCacheTime},
		MaxCacheSizeMB:     maxCacheSize,
		DBMaxConns:         int32(parseInt(getenv("DB_MAX_CONNS", "20"))),
		MaxProxyBodyBytes:  parseInt(getenv("MAX_PROXY_BODY_BYTES", "1048576")), // 1 MiB
	}
	if cfg.GithubClientID == "" || cfg.GithubClientSecret == "" {
		log.Println("warning: GitHub OAuth env vars not set; donating tokens won't work")
	}
	return cfg
}

func parseInt(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil { return 0 }
	return v
}

func loadDotenv() error {
	// Try to load .env explicitly; ignore errors if missing
	_ = godotenv.Load(".env")
	return nil
}
