// Package web serves the dashboard page and its JSON API.
package web

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/hamshoes/devbox-tools/tools/unwrapped/internal/stats"
)

//go:embed index.html
var indexHTML []byte

// Handler returns the HTTP handler for the dashboard.
func Handler(c *stats.Collector) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /api/stats", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(c.Snapshot(r.Context()))
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	return mux
}
