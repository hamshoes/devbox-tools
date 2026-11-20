// Command unwrapped serves a web dashboard of the VM's status.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/tools/unwrapped/internal/stats"
	"github.com/hamshoes/devbox-tools/tools/unwrapped/internal/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "unwrapped:", err)
		os.Exit(1)
	}
}

func run() error {
	addr := flag.String("addr", "0.0.0.0:8089", "listen address")
	home := flag.String("home", "/home/sam", "folder whose contents are measured")
	cpuInterval := flag.Duration("cpu-interval", 2*time.Second, "CPU sampling interval")
	cpuHistory := flag.Int("cpu-history", 150, "CPU samples kept")
	walkInterval := flag.Duration("walk-interval", 10*time.Minute, "how often the home folder size is recalculated")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	if *cpuInterval <= 0 || *walkInterval <= 0 || *cpuHistory < 1 {
		return errors.New("intervals and history must be positive")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	c := stats.New(stats.Config{Version: version, Home: *home, CPUInterval: *cpuInterval, CPUHistory: *cpuHistory, WalkInterval: *walkInterval})
	c.Start(ctx)

	srv := &http.Server{Addr: *addr, Handler: web.Handler(c)}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("unwrapped listening", "addr", *addr, "home", *home, "version", version)

	select {
	case err := <-errc:
		stop()
		c.Wait()
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutCtx)
	c.Wait()
	return err
}
