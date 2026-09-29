// Package server wires the HTTP routes served by Originless.
package server

import (
	"context"
	_ "embed"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/besoeasy/originless/internal/config"
	"github.com/besoeasy/originless/internal/events"
	"github.com/besoeasy/originless/internal/ipfs"
)

const requestTimeout = 5 * time.Second

//go:embed static/index.html
var indexHTML []byte

func serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/index.html" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(indexHTML); err != nil {
		log.Printf("write home page: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("encode JSON response: %v", err)
	}
}

// corsMiddleware allows any origin. Originless holds no credentials, so
// there is nothing for a browser to withhold: a cross-origin caller can reach
// the API exactly as a direct one can, by design.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, HEAD")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewRouter builds a router with the default configuration. It is convenient
// for tests and for callers that do not need to tune anything.
func NewRouter(client *ipfs.Client) http.Handler {
	return NewRouterWithOptions(context.Background(), client, config.Default())
}

// NewRouterWithOptions builds the router.
//
// ctx bounds the lifetime of background work, so cancelling it stops the
// expired-event reaper. Expired events are never served; the reaper only
// reclaims their memory.
func NewRouterWithOptions(ctx context.Context, client *ipfs.Client, cfg config.Config) http.Handler {
	mux := http.NewServeMux()
	store := events.NewStoreWithLimit(cfg.MaxEvents)
	store.StartReaper(ctx, events.ReapInterval)
	mux.HandleFunc("/", serveIndex)
	mux.Handle("/stats", &statsHandler{client: client, events: store})
	mux.Handle("/up", &uploadHandler{client: client, cfg: cfg})
	mux.Handle("/upf", &uploadHandler{client: client, cfg: cfg, folder: true})
	mux.Handle("/cid/{cid}", &cidHandler{client: client})
	mux.Handle("/events", events.NewHandler(store))
	mux.Handle("/events/", events.NewHandler(store))
	mux.HandleFunc("/healthz", healthzHandler(client))
	return corsMiddleware(mux)
}

// healthzHandler reports liveness of both the app and the IPFS node it depends
// on. A container whose node is unreachable cannot serve uploads, downloads or
// stats, so reporting it healthy would be misleading.
func healthzHandler(client *ipfs.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if client == nil {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ipfs": "unchecked"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		if _, err := client.RepoStats(ctx); err != nil {
			log.Printf("health check: IPFS node unavailable: %v", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"status": "degraded",
				"ipfs":   "unavailable",
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "ipfs": "ok"})
	}
}
