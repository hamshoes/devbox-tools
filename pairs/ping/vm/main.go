// Command ping-vm serves GET /ping, tells its mac peer about every request, and answers Ping.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pingv1 "github.com/hamshoes/devbox-tools/pairs/ping/gen/ping/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ping-vm:", err)
		os.Exit(1)
	}
}

func run() error {
	httpAddr := flag.String("http", "0.0.0.0:8082", "HTTP listen address")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	gw, appSock, err := tetherapp.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/ping.v1.PingControl/Ping", func(_ context.Context, r *pingv1.PingRequest) (*pingv1.PingResponse, error) {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		return &pingv1.PingResponse{Pong: at, At: at}, nil
	})
	errc := make(chan error, 2)
	go func() { errc <- app.Serve(ctx, appSock) }()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"pong": true, "at": time.Now().UTC().Format(time.RFC3339Nano)})
	})
	srv := &http.Server{Addr: *httpAddr, Handler: report(gw, log, mux), ReadHeaderTimeout: 10 * time.Second}
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("http listening", "addr", *httpAddr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(sctx)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// report calls RequestSeen for every request, once its response is written.
func report(gw string, log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		at := time.Now().UTC()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		err := tetherapp.Call(ctx, gw, "/ping.v1.PingEvents/RequestSeen", &pingv1.RequestSeenRequest{
			Method: r.Method, Route: r.URL.Path, Status: int32(rec.status), At: at.Format(time.RFC3339Nano),
		}, &pingv1.RequestSeenResponse{})
		if e, ok := err.(*tetherapp.Error); ok && e.Code == tetherapp.CodeUnavailable {
			log.Debug("no mac peer; dropped event", "route", r.URL.Path)
		} else if err != nil {
			log.Warn("RequestSeen failed", "err", err)
		}
	})
}
