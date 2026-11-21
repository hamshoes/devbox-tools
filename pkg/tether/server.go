package tether

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type ServerConfig struct {
	Addr         string        // e.g. "0.0.0.0:8081"
	Path         string        // default "/tether"
	Token        string        // required
	PingInterval time.Duration // default 20s
	PingTimeout  time.Duration // default 10s
	MaxMessage   int64         // default 1 MiB
}

type server struct {
	*base
	token  string
	http   *http.Server
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	busy   bool
	closed bool
	wg     sync.WaitGroup

	closeOnce sync.Once
}

// Listen starts a server and returns once it is listening. It stops on Close
// or when ctx is cancelled. Its state is Connecting while it waits for a client.
func Listen(ctx context.Context, cfg ServerConfig, r *Router, log *slog.Logger) (Peer, error) {
	if cfg.Token == "" {
		return nil, errors.New("tether: server token is required")
	}
	if cfg.Path == "" {
		cfg.Path = "/tether"
	}
	if !strings.HasPrefix(cfg.Path, "/") {
		return nil, fmt.Errorf("tether: path %q must start with /", cfg.Path)
	}
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return nil, fmt.Errorf("tether: listen: %w", err)
	}

	s := &server{base: newBase(r, log, cfg.PingInterval, cfg.PingTimeout, cfg.MaxMessage), token: cfg.Token}
	s.ctx, s.cancel = context.WithCancel(ctx)
	mux := http.NewServeMux()
	mux.HandleFunc(cfg.Path, s.handle)
	s.http = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	s.log.Debug("tether: listening", "addr", ln.Addr().String(), "path", cfg.Path)
	go func() {
		if err := s.http.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("tether: server stopped", "err", err)
			s.Close()
		}
	}()
	go func() {
		<-s.ctx.Done()
		s.Close()
	}()
	return s, nil
}

func (s *server) handle(w http.ResponseWriter, req *http.Request) {
	got, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		http.Error(w, "shutting down", http.StatusServiceUnavailable)
		return
	}
	if s.busy {
		s.mu.Unlock()
		http.Error(w, "a client is already connected", http.StatusConflict)
		return
	}
	s.busy = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		s.busy = false
		s.mu.Unlock()
	}()

	conn, err := websocket.Accept(w, req, nil)
	if err != nil {
		s.log.Debug("tether: upgrade failed", "err", err)
		return
	}
	s.run(s.ctx, conn)
	s.bus.set(Connecting)
}

func (s *server) Close() error {
	s.closeOnce.Do(func() {
		deadline := time.Now().Add(closeBudget)
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.cancel()

		sctx, scancel := context.WithDeadline(context.Background(), deadline)
		defer scancel()
		if err := s.http.Shutdown(sctx); err != nil {
			s.http.Close()
		}
		waited := make(chan struct{})
		go func() { s.wg.Wait(); close(waited) }()
		select {
		case <-waited:
		case <-sctx.Done():
		}
		s.bus.set(Closed)
		select {
		case <-s.bus.done:
		case <-sctx.Done():
		}
	})
	return nil
}
