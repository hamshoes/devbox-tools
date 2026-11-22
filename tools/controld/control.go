package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

// startControl runs the control plane: a supervised control gateway, the
// Control service behind it (pairs, and services through svc), the pair
// manager and the port watcher. It does nothing, and says so, if tether-gw or
// the control pair is not installed; the returned *controlPlane is then nil.
// The returned func stops everything and waits.
func startControl(parent context.Context, log *slog.Logger, cfg *platform.Config, token string, svc *serviceMgr, pr *problems) (*controlPlane, func()) {
	root := filepath.Join(cfg.DevtoolsDir, "pairs")
	state := cfg.TetherStateDir
	gwBin := findBin(cfg, "tether-gw")
	dir, err := filepath.EvalSymlinks(filepath.Join(root, "control", "current"))
	switch {
	case gwBin == "":
		log.Info("control plane disabled: tether-gw is not installed")
		return nil, func() {}
	case err != nil:
		log.Info("control plane disabled: control pair is not installed", "err", err)
		return nil, func() {}
	}
	cdir := filepath.Join(state, "control")
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		log.Warn("control plane disabled", "err", err)
		return nil, func() {}
	}
	tokenFile := filepath.Join(cdir, "token")
	if err := writeSecret(tokenFile, token); err != nil {
		log.Warn("control plane disabled", "err", err)
		return nil, func() {}
	}
	gwSock, appSock := filepath.Join(cdir, "gw.sock"), filepath.Join(cdir, "app.sock")

	ip, err := containerIP()
	if err != nil {
		log.Warn("container ip", "err", err)
	}
	ctx, cancel := context.WithCancel(parent)
	mgr := &pairMgr{
		log: log, cfg: cfg, host: ip, gwBin: gwBin, root: root, state: state,
		control: gwSock, problems: pr, ctx: ctx, pairs: map[string]*runningPair{}, last: map[string]*controlv1.PairResult{},
	}

	var wg sync.WaitGroup
	exits := 0
	gw := &supervised{
		log: log.With("proc", "control-gw"), logf: filepath.Join(cdir, "gw.log"), env: childEnv(),
		argv: []string{gwBin, "--side", "vm", "--pair-dir", dir, "--listen", fmt.Sprintf("0.0.0.0:%d", cfg.ControlPort),
			"--token-file", tokenFile, "--gw-socket", gwSock, "--app-socket", appSock},
		onExit: func() bool {
			if exits++; exits%crashLimit == 0 {
				log.Error("control gateway is crash-looping", "exits", exits)
			}
			return true
		},
	}
	wg.Go(func() { gw.run(ctx) })

	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/control.v1.Control/ApplyPairs", mgr.ApplyPairs)
	tetherapp.Handle(app, "/control.v1.Control/ApplyServices", svc.ApplyServices)
	tetherapp.Handle(app, "/control.v1.Control/Health", func(context.Context, *controlv1.HealthRequest) (*controlv1.HealthResponse, error) {
		return &controlv1.HealthResponse{Side: "vm", Version: version, At: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
	wg.Go(func() {
		if err := app.Serve(ctx, appSock); err != nil {
			log.Error("control app stopped", "err", err)
		}
	})
	wg.Go(func() { mgr.watchControl(ctx) })
	watcher := &portWatcher{log: log.With("proc", "ports"), cfg: cfg, gwSock: gwSock}
	wg.Go(func() { watcher.run(ctx) })
	log.Info("control plane", "port", cfg.ControlPort, "pair_dir", dir, "gateway", gwBin)

	return &controlPlane{cfg: cfg, gwSock: gwSock, pairs: mgr, ip: ip}, func() {
		mgr.stopAll("controld shutting down")
		cancel()
		wg.Wait()
	}
}
