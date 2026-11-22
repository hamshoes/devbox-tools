package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hamshoes/devbox-tools/pairs/notify/approve"
	"github.com/hamshoes/devbox-tools/pairs/notify/internal/clean"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

// serveApprovals serves approve.Path on the unix socket sock (mode 0600) until ctx ends.
func (m *mac) serveApprovals(ctx context.Context, sock string) error {
	ln, err := tetherapp.ListenUnix(sock)
	if err != nil {
		return fmt.Errorf("approval socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+approve.Path, m.approve)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
	}()
	m.log.Info("approval socket", "path", sock)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("approval socket: %w", err)
	}
	return nil
}

// approve queues a dialog for a Mac pair app and answers once it is decided.
// If the caller goes away first, the dialog is withdrawn.
func (m *mac) approve(w http.ResponseWriter, r *http.Request) {
	var req approve.Request
	if err := json.NewDecoder(io.LimitReader(r.Body, approve.MaxRequest)).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	title, body := clean.Line(req.Title, 120), clean.Text(req.Body, 2000)
	if title == "" {
		http.Error(w, "empty title", http.StatusBadRequest)
		return
	}

	m.mu.Lock()
	active := 0
	for _, it := range m.items {
		if it.state != stateAnswered {
			active++
		}
	}
	if active >= maxActive {
		m.mu.Unlock()
		http.Error(w, errQueueFull.Error(), http.StatusTooManyRequests)
		return
	}
	m.nextID++
	it := &item{id: fmt.Sprintf("local-%d", m.nextID), title: "devbox · " + title, body: body, local: make(chan bool, 1)}
	m.enqueueLocked(it)
	m.mu.Unlock()
	m.log.Info("approval queued", "id", it.id, "title", title)

	var allow bool
	select {
	case allow = <-it.local:
	case <-r.Context().Done():
		m.mu.Lock()
		m.withdrawLocked(it.id)
		m.mu.Unlock()
		m.log.Info("approval withdrawn: caller gone", "id", it.id)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(approve.Response{Allow: allow})
}
