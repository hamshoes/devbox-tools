// Command controld is the VM half of the devbox control plane. It runs the
// control pair's gateway and service, starts and stops the other pairs the Mac
// asks for, watches listening ports, and serves bb on a local unix socket.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/hamshoes/devbox-tools/pkg/platform"
)

var version = "dev"

// tokenEnv names the control pair's token (P9: the name stays DISPATCH_TOKEN).
const tokenEnv = "DISPATCH_TOKEN"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "controld:", err)
		os.Exit(1)
	}
}

func run() error {
	debug := flag.Bool("debug", false, "log at debug level")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}

	lvl := slog.LevelInfo
	if *debug {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	cfg, err := platform.Load()
	if err != nil {
		return err
	}
	envFile := filepath.Join(cfg.ConfigDir, "env")
	token, err := loadToken(envFile)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no token: set %s in the environment or in %s", tokenEnv, envFile)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pr := &problems{log: log}
	svc := newServiceMgr(ctx, log.With("proc", "services"), cfg, pr)
	svc.start()
	defer svc.stopAll()

	cp, stopControl := startControl(ctx, log, cfg, token, svc, pr)
	defer stopControl()

	local := &localAPI{log: log.With("proc", "local"), cp: cp, svc: svc, problems: pr}
	log.Info("listening", "socket", cfg.ControldSocket)
	if err := local.serve(ctx, cfg.ControldSocket); err != nil {
		return fmt.Errorf("local socket: %w", err)
	}
	log.Info("shutting down")
	return nil
}

// loadToken reads the token from the environment, then from envFile
// (KEY=value lines, optionally prefixed by "export" and quoted).
func loadToken(envFile string) (string, error) {
	if t := os.Getenv(tokenEnv); t != "" {
		return t, nil
	}
	f, err := os.Open(envFile)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != tokenEnv {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		return v, nil
	}
	return "", sc.Err()
}
