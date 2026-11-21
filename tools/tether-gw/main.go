// Command tether-gw is the generic gateway for one tether pair on one side: apps
// call it over a unix socket, and it relays checked calls to its peer gateway.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/tether"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

var version = "dev"

const (
	exitConfig = 2
	exitRun    = 1
)

func main() {
	var (
		side      = flag.String("side", "", "vm or mac")
		pairDir   = flag.String("pair-dir", "", "directory with manifest.json and api.binpb")
		listen    = flag.String("listen", "", "vm side: websocket listen address, host:port")
		dialUnix  = flag.String("dial-unix", "", "mac side: unix socket to dial the websocket through")
		tokenFile = flag.String("token-file", "", "file with the pair token (mode 0600)")
		gwSocket  = flag.String("gw-socket", "", "unix socket apps call")
		appSocket = flag.String("app-socket", "", "unix socket of the local app, for inbound calls")
		logLevel  = flag.String("log-level", "info", "debug, info, warn or error")
		showVer   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVer {
		fmt.Println(version)
		return
	}
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		die(exitConfig, "bad --log-level: %v", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))

	switch {
	case *side != "vm" && *side != "mac":
		die(exitConfig, "--side must be vm or mac")
	case *pairDir == "" || *tokenFile == "" || *gwSocket == "":
		die(exitConfig, "--pair-dir, --token-file and --gw-socket are required")
	case *side == "vm" && *listen == "":
		die(exitConfig, "--listen is required on the vm side")
	case *side == "mac" && *dialUnix == "":
		die(exitConfig, "--dial-unix is required on the mac side")
	}

	sc, err := loadSchema(*pairDir)
	if err != nil {
		die(exitConfig, "pair %s: %v", *pairDir, err)
	}
	token, err := readToken(*tokenFile)
	if err != nil {
		die(exitConfig, "%v", err)
	}
	log = log.With("pair", sc.man.Pair, "side", *side)
	log.Info("starting", "version", sc.man.Version, "gateway", version, "schema_sha256", sc.sha)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	g := newGateway(*side, sc, *appSocket, log)
	router := tether.NewRouter(log)
	tether.Handle(router, g.handleHello)
	tether.Handle(router, g.handleCall)
	maxMsg := sc.man.Limits.MaxMessage + 4096 // envelope overhead

	if *side == "vm" {
		g.peer, err = tether.Listen(ctx, tether.ServerConfig{Addr: *listen, Path: "/tether", Token: token, MaxMessage: maxMsg}, router, log)
	} else {
		sock := *dialUnix
		hc := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		}}
		g.peer, err = tether.Dial(ctx, tether.ClientConfig{URL: "ws://tether-gw/tether", Token: token, MaxMessage: maxMsg, HTTPClient: hc}, router, log)
	}
	if err != nil {
		die(exitRun, "%v", err)
	}
	g.peer.OnState(func(st tether.State) { g.onState(ctx, st) })

	ln, err := tetherapp.ListenUnix(*gwSocket)
	if err != nil {
		g.peer.Close()
		die(exitRun, "gw-socket: %v", err)
	}
	srv := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
	}()
	err = srv.Serve(ln)
	os.Remove(*gwSocket)
	g.peer.Close()
	if err != nil && err != http.ErrServerClosed {
		die(exitRun, "gw-socket: %v", err)
	}
}

func readToken(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file %s has mode %o; it must be 0600", path, st.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}

func die(code int, format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tether-gw: "+format+"\n", args...)
	os.Exit(code)
}
