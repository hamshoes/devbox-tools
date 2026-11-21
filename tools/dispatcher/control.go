package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/tether"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const controlPort = 4100

// startControl runs the control plane next to everything else the dispatcher
// does: a supervised control gateway, and the Control service behind it. It does
// nothing, and says so, if tether-gw or the control pair is not installed. The
// returned func stops everything and waits.
func startControl(parent context.Context, log *slog.Logger, peer tether.Peer, host, token string) func() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Warn("control plane disabled", "err", err)
		return func() {}
	}
	root := filepath.Join(home, ".local/share/devtools/pairs")
	state := filepath.Join(home, ".local/state/tether")
	gwBin := findBin("tether-gw")
	dir, err := filepath.EvalSymlinks(filepath.Join(root, "control", "current"))
	switch {
	case gwBin == "":
		log.Info("control plane disabled: tether-gw is not installed")
		return func() {}
	case err != nil:
		log.Info("control plane disabled: control pair is not installed", "err", err)
		return func() {}
	}
	cdir := filepath.Join(state, "control")
	if err := os.MkdirAll(cdir, 0o700); err != nil {
		log.Warn("control plane disabled", "err", err)
		return func() {}
	}
	tokenFile := filepath.Join(cdir, "token")
	if err := writeSecret(tokenFile, token); err != nil {
		log.Warn("control plane disabled", "err", err)
		return func() {}
	}
	gwSock, appSock := filepath.Join(cdir, "gw.sock"), filepath.Join(cdir, "app.sock")

	ctx, cancel := context.WithCancel(parent)
	mgr := &pairMgr{
		log: log, peer: peer, host: host, gwBin: gwBin, root: root, state: state,
		control: gwSock, ctx: ctx, pairs: map[string]*runningPair{},
	}

	var wg sync.WaitGroup
	exits := 0
	gw := &supervised{
		log: log.With("proc", "control-gw"), logf: filepath.Join(cdir, "gw.log"), env: childEnv(),
		argv: []string{gwBin, "--side", "vm", "--pair-dir", dir, "--listen", "0.0.0.0:4100",
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
	tetherapp.Handle(app, "/control.v1.Control/Health", func(context.Context, *controlv1.HealthRequest) (*controlv1.HealthResponse, error) {
		return &controlv1.HealthResponse{Side: "vm", Version: version, At: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
	wg.Go(func() {
		if err := app.Serve(ctx, appSock); err != nil {
			log.Error("control app stopped", "err", err)
		}
	})
	wg.Go(func() { mgr.watchControl(ctx) })
	log.Info("control plane", "port", controlPort, "pair_dir", dir, "gateway", gwBin)

	return func() {
		mgr.stopAll("dispatcher shutting down")
		cancel()
		wg.Wait()
	}
}
