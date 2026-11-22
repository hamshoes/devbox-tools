package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	notifyv1 "github.com/hamshoes/devbox-tools/pairs/notify/gen/notify/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const (
	reportEvery    = 5 * time.Second
	syncCheckEvery = 10 * time.Minute
	maxPending     = 50
	maxPerFlush    = 5 // the rest are folded into one summary
)

// reporter sends problems to the Mac through the notify pair. Problems raised
// while no session is connected wait and go out when one connects. Queueing
// never blocks; past maxPending the oldest are dropped.
type reporter struct {
	log      *slog.Logger
	cfg      *platform.Config
	problems *problems
	gwSock   string // the notify pair's VM gateway

	mu      sync.Mutex
	pending []*controlv1.Problem
	wake    chan struct{}
}

func newReporter(log *slog.Logger, cfg *platform.Config, pr *problems) *reporter {
	return &reporter{
		log: log, cfg: cfg, problems: pr,
		gwSock: filepath.Join(cfg.TetherStateDir, "notify", "gw.sock"),
		wake:   make(chan struct{}, 1),
	}
}

func (r *reporter) enqueue(p *controlv1.Problem) {
	r.mu.Lock()
	r.pending = append(r.pending, p)
	if n := len(r.pending); n > maxPending {
		r.pending = r.pending[n-maxPending:]
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// run flushes pending reports whenever the notify gateway has a peer, and
// checks the platform sync's state, until ctx ends.
func (r *reporter) run(ctx context.Context) {
	t := time.NewTicker(reportEvery)
	defer t.Stop()
	var lastSyncErr string
	var nextSync time.Time
	for {
		if time.Now().After(nextSync) {
			nextSync = time.Now().Add(syncCheckEvery)
			msg := r.syncError()
			if msg != "" && msg != lastSyncErr {
				r.problems.add("sync", "%s", msg)
			}
			lastSyncErr = msg
		}
		r.flush(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

// syncError is the last platform sync's error from its state file, or "".
func (r *reporter) syncError() string {
	raw, err := os.ReadFile(filepath.Join(r.cfg.DevtoolsDir, "state.json"))
	if err != nil {
		return ""
	}
	var st struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &st) != nil || st.OK == nil || *st.OK {
		return ""
	}
	if st.Error == "" {
		return "platform sync failed"
	}
	return "platform sync failed: " + st.Error
}

func (r *reporter) flush(ctx context.Context) {
	r.mu.Lock()
	n := len(r.pending)
	r.mu.Unlock()
	if n == 0 || !gwConnected(ctx, r.gwSock) {
		return
	}
	r.mu.Lock()
	batch := r.pending
	r.pending = nil
	r.mu.Unlock()

	var reqs []*notifyv1.NotifyRequest
	for i, p := range batch {
		if i == maxPerFlush-1 && len(batch) > maxPerFlush {
			reqs = append(reqs, &notifyv1.NotifyRequest{
				Title: fmt.Sprintf("%d more problems", len(batch)-i),
				Text:  "run bb status on the VM",
			})
			break
		}
		reqs = append(reqs, &notifyv1.NotifyRequest{Title: p.Source, Text: p.Message})
	}
	for i, req := range reqs {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := tetherapp.Call(cctx, r.gwSock, "/notify.v1.Notify/Notify", req, &notifyv1.NotifyResponse{})
		cancel()
		if err != nil {
			r.log.Info("report not delivered; will retry", "err", err)
			if i < len(batch) {
				r.mu.Lock()
				r.pending = append(batch[i:], r.pending...)
				r.mu.Unlock()
			}
			return
		}
	}
}
