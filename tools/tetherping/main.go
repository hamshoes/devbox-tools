// Command tetherping serves GET /ping and tells the connected tether client about every HTTP request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/tether"
	"github.com/hamshoes/devbox-tools/pkg/tether/examples/demo"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "tetherping:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	httpAddr := flag.String("http", "0.0.0.0:8082", "HTTP listen address")
	links := flag.String("links", filepath.Join(home, ".config/devbox/links.toml"), "links file")
	link := flag.String("link", "demo", "link name in the links file")
	level := flag.String("log-level", "debug", "log level: debug, info, warn or error")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*level)); err != nil {
		return fmt.Errorf("-log-level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	srvCfg, _, err := tether.LoadConfig(*links, *link)
	if err != nil {
		return err
	}
	if srvCfg == nil {
		return fmt.Errorf("link %q in %s must set listen, not dial", *link, *links)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	peer, err := tether.Listen(ctx, *srvCfg, tether.NewRouter(log), log)
	if err != nil {
		return err
	}
	defer peer.Close()
	peer.OnState(func(s tether.State) { log.Info("tether", "state", s) })

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"pong": true, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	})
	srv := &http.Server{Addr: *httpAddr, Handler: report(peer, log, mux), ReadHeaderTimeout: 10 * time.Second}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("http listening", "addr", *httpAddr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return peer.Close()
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// report sends a RequestSeen for every request, once its response is written.
func report(peer tether.Peer, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now().UTC()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Debug("request", "method", r.Method, "route", r.URL.Path, "status", rec.status)

		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		err := peer.Send(ctx, demo.RequestSeen{Method: r.Method, Route: r.URL.Path, Status: rec.status, At: at})
		if errors.Is(err, tether.ErrNotConnected) {
			log.Debug("no tether client; dropped event", "route", r.URL.Path)
		} else if err != nil {
			log.Warn("tether send failed", "err", err)
		}
	})
}
