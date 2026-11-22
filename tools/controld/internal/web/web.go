// Package web serves controld's page on BOX_WEB_PORT: the status page at
// /, /ping, and a read-only JSON API. Changes go through bb, never here.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/tools/controld/internal/stats"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

//go:embed index.html
var indexHTML []byte

// Source is what controld adds to the VM's stats.
type Source struct {
	// Status is controld's own state: services, pairs, problems.
	Status func(ctx context.Context) *controlv1.StatusResponse
	// LockSHA is the sha256 of the image's devbox.lock, or "".
	LockSHA func() string
	// Sync is the platform sync's state file as it stands, or nil.
	Sync func() json.RawMessage
}

var pj = protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}

// Handler returns the HTTP handler for the page and its API.
func Handler(c *stats.Collector, src Source) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(indexHTML)
	})
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"pong": true, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	})
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		st, err := pj.Marshal(src.Status(r.Context()))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{
			"stats":    c.Snapshot(r.Context()),
			"controld": json.RawMessage(st),
			"lock_sha": src.LockSHA(),
			"sync":     src.Sync(),
		})
	})
	mux.HandleFunc("GET /api/services", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, "services", src.Status(r.Context()).Services)
	})
	mux.HandleFunc("GET /api/pairs", func(w http.ResponseWriter, r *http.Request) {
		writeList(w, "pairs", src.Status(r.Context()).Pairs)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// writeList writes {"<key>": [...]} with each message in protojson.
func writeList[M proto.Message](w http.ResponseWriter, key string, list []M) {
	out := make([]json.RawMessage, 0, len(list))
	for _, m := range list {
		raw, err := pj.Marshal(m)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out = append(out, raw)
	}
	writeJSON(w, map[string]any{key: out})
}
