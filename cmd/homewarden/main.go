// Command homewarden is the Homewarden server.
//
// For now it only serves a placeholder page and a health endpoint, so the image build
// and the container setup can be tested end to end before the real application exists.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// Set by the image build via -ldflags.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

const placeholder = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Homewarden</title>
<style>
:root{color-scheme:light dark;--bg:#f3f5f8;--fg:#16202e;--muted:#5b6778;--accent:#2f56c9}
@media (prefers-color-scheme:dark){:root{--bg:#0f141c;--fg:#e4e9f1;--muted:#95a1b3;--accent:#86a3ff}}
body{margin:0;min-height:100vh;display:grid;place-items:center;background:var(--bg);color:var(--fg);font:16px/1.5 system-ui,sans-serif}
main{text-align:center;padding:24px}
svg{width:72px;height:72px;color:var(--accent)}
p{color:var(--muted)}
</style></head><body><main>
<svg viewBox="0 0 48 48" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M6 22 24 7l18 15"/><path d="M10 19v21h28V19"/><path d="M13 30h6l3-6 4 11 3-5h6"/></svg>
<h1>Homewarden</h1>
<p>The container is running. The application is still being built.</p>
</main></body></html>`

func main() {
	addr := env("HOMEWARDEN_ADDR", ":8080")
	dataDir := env("HOMEWARDEN_DATA", "/data")

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		log.Fatalf("data directory %s: %v", dataDir, err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"version":   version,
			"commit":    commit,
			"buildDate": buildDate,
		})
	})
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholder))
	})

	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("homewarden %s (%s, built %s) listening on %s, data in %s", version, commit, buildDate, addr, dataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
