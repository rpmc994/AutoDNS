package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"autodns/internal/api"
	"autodns/internal/monitor"
	"autodns/internal/store"
)

//go:embed web
var webFS embed.FS

func main() {
	// --- Data directory ---
	dataDir := os.Getenv("DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0750); err != nil {
		log.Fatalf("creating data dir: %v", err)
	}

	dbPath := filepath.Join(dataDir, "autodns.db")

	// --- Store ---
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("opening store: %v", err)
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Printf("closing store: %v", err)
		}
	}()

	// --- Monitor ---
	mon := monitor.New(st)
	mon.Start()
	defer mon.Stop()

	// --- HTTP mux ---
	mux := http.NewServeMux()
	authToken := strings.TrimSpace(os.Getenv("AUTODNS_AUTH_TOKEN"))
	if authToken == "" {
		log.Println("WARNING: AUTODNS_AUTH_TOKEN is not set; /api endpoints are unauthenticated")
	}

	// Register API routes.
	api.New(st, mon, mux, authToken)

	// Serve the embedded SPA for all other paths.
	webRoot, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("sub-filesystem for web: %v", err)
	}
	mux.Handle("/", http.FileServer(http.FS(webRoot)))

	// --- Server ---
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr:         addr,
		Handler:      secureHeaders(mux),
		ReadTimeout:  10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	// Start in a goroutine so we can wait for a shutdown signal.
	go func() {
		log.Printf("AutoDNS listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	// Graceful shutdown on SIGINT / SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
