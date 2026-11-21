// Command ping-mac logs the requests ping-vm reports, and pings it every 30 seconds.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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
		fmt.Fprintln(os.Stderr, "ping-mac:", err)
		os.Exit(1)
	}
}

func run() error {
	every := flag.Duration("every", 30*time.Second, "how often to call Ping")
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
	tetherapp.Handle(app, "/ping.v1.PingEvents/RequestSeen", func(_ context.Context, r *pingv1.RequestSeenRequest) (*pingv1.RequestSeenResponse, error) {
		lag := time.Duration(0)
		if at, err := time.Parse(time.RFC3339Nano, r.At); err == nil {
			lag = time.Since(at)
		}
		log.Info("request", "method", r.Method, "route", r.Route, "status", r.Status, "lag", lag)
		return &pingv1.RequestSeenResponse{}, nil
	})
	errc := make(chan error, 1)
	go func() { errc <- app.Serve(ctx, appSock) }()

	go func() {
		t := time.NewTicker(*every)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				start := time.Now()
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				err := tetherapp.Call(cctx, gw, "/ping.v1.PingControl/Ping", &pingv1.PingRequest{Note: "mac"}, &pingv1.PingResponse{})
				cancel()
				if err != nil {
					log.Warn("ping failed", "err", err)
				} else {
					log.Info("ping", "rtt", time.Since(start))
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
}
