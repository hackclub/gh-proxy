package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gh-proxy/internal/config"
	"gh-proxy/internal/db"
	"gh-proxy/internal/server"
)

func main() {
	cfg := config.Load()

	pool, err := db.Connect(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(context.Background(), pool); err != nil {
		log.Fatalf("db migrate: %v", err)
	}

	srv := server.New(pool, cfg)

	httpServer := &http.Server{
		Addr:              ":8080",
		Handler:           srv.Router,
		ReadHeaderTimeout: 5 * time.Second,   // Faster header reading
		ReadTimeout:       15 * time.Second,  // Faster read timeout
		WriteTimeout:      30 * time.Second,  // Faster write timeout
		IdleTimeout:       60 * time.Second,  // Shorter idle timeout
		MaxHeaderBytes:    64 << 10,          // 64KB max headers
	}

	go func() {
		log.Printf("listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	// start background jobs
	go srv.LogsJanitor()

	// graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}
