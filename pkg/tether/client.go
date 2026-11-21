package tether

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const handshakeTimeout = 10 * time.Second

type ClientConfig struct {
	URL          string        // e.g. "ws://127.0.0.1:18081/tether"
	Token        string        // required
	MinBackoff   time.Duration // default 500ms
	MaxBackoff   time.Duration // default 30s
	PingInterval time.Duration // default 20s
	PingTimeout  time.Duration // default 10s
	MaxMessage   int64         // default 1 MiB
}

type client struct {
	*base
	url    string
	header http.Header
	min    time.Duration
	max    time.Duration
	ctx    context.Context
	cancel context.CancelFunc
	loop   chan struct{}

	closeOnce sync.Once
}

// Dial returns immediately and connects in the background, retrying with
// jittered exponential backoff. Failed connects are never fatal, except a
// 401, which logs once and moves to Closed.
func Dial(ctx context.Context, cfg ClientConfig, r *Router, log *slog.Logger) (Peer, error) {
	if cfg.Token == "" {
		return nil, errors.New("tether: client token is required")
	}
	if cfg.URL == "" {
		return nil, errors.New("tether: client URL is required")
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 30 * time.Second
	}
	cfg.MaxBackoff = max(cfg.MaxBackoff, cfg.MinBackoff)

	c := &client{
		base:   newBase(r, log, cfg.PingInterval, cfg.PingTimeout, cfg.MaxMessage),
		url:    cfg.URL,
		header: http.Header{"Authorization": {"Bearer " + cfg.Token}},
		min:    cfg.MinBackoff,
		max:    cfg.MaxBackoff,
		loop:   make(chan struct{}),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	go c.connectLoop()
	go func() {
		<-c.ctx.Done()
		c.Close()
	}()
	return c, nil
}

func (c *client) connectLoop() {
	defer close(c.loop)
	defer c.bus.set(Closed)
	backoff := c.min
	for c.ctx.Err() == nil {
		hctx, cancel := context.WithTimeout(c.ctx, handshakeTimeout)
		conn, resp, err := websocket.Dial(hctx, c.url, &websocket.DialOptions{HTTPHeader: c.header})
		cancel()
		if err == nil {
			backoff = c.min
			c.run(c.ctx, conn)
			c.bus.set(Reconnecting)
		} else {
			if resp != nil && resp.StatusCode == http.StatusUnauthorized {
				c.log.Error("tether: server rejected the token (401); giving up", "url", c.url)
				c.cancel()
				return
			}
			c.log.Debug("tether: connect failed", "url", c.url, "err", err)
			backoff = min(backoff*2, c.max)
		}
		if !sleep(c.ctx, jitter(backoff)) {
			return
		}
	}
}

// jitter spreads d by ±20%.
func jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *client) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		budget := time.NewTimer(closeBudget)
		defer budget.Stop()
		for _, ch := range []chan struct{}{c.loop, c.bus.done} {
			select {
			case <-ch:
			case <-budget.C:
				return
			}
		}
	})
	return nil
}
