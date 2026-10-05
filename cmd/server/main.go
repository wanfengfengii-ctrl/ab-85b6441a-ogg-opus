// Command server runs the Ogg Opus audit HTTP API.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/acoustics/opus-audit/internal/httpapi"
)

func main() {
	addr := flag.String("addr", envOr("OPUS_AUDIT_ADDR", ":8080"),
		"listen address (overrides $OPUS_AUDIT_ADDR)")
	flag.Parse()

	logger := log.New(os.Stdout, "opus-audit ", log.LstdFlags|log.Lmicroseconds)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           httpapi.NewServer(logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Printf("listening on %s", *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Fatalf("server error: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
