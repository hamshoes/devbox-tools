package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/tools/controld/internal/stats"
	"github.com/hamshoes/devbox-tools/tools/controld/internal/web"
)

// servePage serves the status page on 0.0.0.0:BOX_WEB_PORT until ctx ends.
// A port that can't be bound is a problem, not a reason to stop controld.
func servePage(ctx context.Context, log *slog.Logger, cfg *platform.Config, pr *problems, status func(context.Context) *controlv1.StatusResponse) {
	home, err := os.UserHomeDir()
	if err != nil {
		pr.add("web", "status page disabled: %v", err)
		return
	}
	col := stats.New(stats.Config{
		Log: log, Version: version, Home: home,
		CPUInterval: 2 * time.Second, CPUHistory: 150, WalkInterval: 10 * time.Minute,
	})
	cctx, stopStats := context.WithCancel(ctx)
	col.Start(cctx)
	defer func() {
		stopStats()
		col.Wait()
	}()

	src := web.Source{
		Status:  status,
		LockSHA: func() string { return fileSHA(cfg.LockFile) },
		Sync:    func() json.RawMessage { return jsonFile(filepath.Join(cfg.DevtoolsDir, "state.json")) },
	}
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.WebPort)
	srv := &http.Server{
		Addr: addr, Handler: web.Handler(col, src),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("status page", "addr", addr)
	select {
	case err := <-errc:
		pr.add("web", "status page on %s: %v", addr, err)
		return
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Warn("status page shutdown", "err", err)
	}
}

// fileSHA is the hex sha256 of a file's bytes, or "" if it can't be read.
func fileSHA(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// jsonFile returns a file's content if it is valid JSON, else nil.
func jsonFile(path string) json.RawMessage {
	raw, err := os.ReadFile(path)
	if err != nil || !json.Valid(raw) {
		return nil
	}
	return raw
}
