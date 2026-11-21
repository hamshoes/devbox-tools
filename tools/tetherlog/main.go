// Command tetherlog connects to tetherping through a tether link and logs every request it reports.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
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
		fmt.Fprintln(os.Stderr, "tetherlog:", err)
		os.Exit(1)
	}
}

func run() error {
	home, _ := os.UserHomeDir()
	links := flag.String("links", filepath.Join(home, ".config/devbox/links.toml"), "links file")
	link := flag.String("link", "demo", "link name in the links file")
	level := flag.String("log-level", "info", "log level: debug, info, warn or error")
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

	_, cliCfg, err := tether.LoadConfig(*links, *link)
	if err != nil {
		return err
	}
	if cliCfg == nil {
		return fmt.Errorf("link %q in %s must set dial, not listen", *link, *links)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := tether.NewRouter(log)
	tether.On(router, func(_ context.Context, m demo.RequestSeen) {
		log.Info("request", "method", m.Method, "route", m.Route, "status", m.Status, "at", m.At, "lag", time.Since(m.At))
	})

	peer, err := tether.Dial(ctx, *cliCfg, router, log)
	if err != nil {
		return err
	}
	defer peer.Close()
	closed := make(chan struct{})
	peer.OnState(func(s tether.State) {
		log.Info("tether", "state", s)
		if s == tether.Closed {
			close(closed)
		}
	})

	select {
	case <-ctx.Done():
		return peer.Close()
	case <-closed:
		return fmt.Errorf("link %q closed", *link)
	}
}
