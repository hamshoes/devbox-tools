// Command dispatcher pushes exec events to the Mac over a tether server, fed by the dispatch CLI over a unix socket.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
	"github.com/hamshoes/devbox-tools/pkg/tether"
)

var version = "dev"

const (
	maxRequest   = 64 << 10
	readDeadline = 5 * time.Second
	sendTimeout  = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "dispatcher:", err)
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

	token := loadToken()
	if token == "" {
		return fmt.Errorf("no token: set %s in the environment or in ~/.config/devbox/env", dispatch.TokenEnv)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	peer, err := tether.Listen(ctx, tether.ServerConfig{
		Addr:  fmt.Sprintf("0.0.0.0:%d", dispatch.DispatcherPort),
		Path:  dispatch.TetherPath,
		Token: token,
	}, tether.NewRouter(log), log)
	if err != nil {
		return err
	}
	defer peer.Close()
	peer.OnState(func(s tether.State) { log.Info("tether", "state", s) })

	sock := dispatch.SocketPath()
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	defer os.Remove(sock)
	defer ln.Close()
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	log.Info("listening", "port", dispatch.DispatcherPort, "socket", sock)

	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				log.Info("shutting down")
				return nil
			}
			return err
		}
		go serve(conn, peer, log)
	}
}

// loadToken reads the token from the environment, then from ~/.config/devbox/env.
func loadToken() string {
	if t := os.Getenv(dispatch.TokenEnv); t != "" {
		return t
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	f, err := os.Open(filepath.Join(home, ".config/devbox/env"))
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != dispatch.TokenEnv {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		return v
	}
	return ""
}

func serve(conn net.Conn, peer tether.Peer, log *slog.Logger) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(readDeadline))
	var req dispatch.Request
	if err := json.NewDecoder(io.LimitReader(conn, maxRequest)).Decode(&req); err != nil {
		reply(conn, dispatch.Response{Error: "bad request"})
		return
	}
	resp := handle(req, peer, log)
	reply(conn, resp)
}

func reply(conn net.Conn, resp dispatch.Response) {
	json.NewEncoder(conn).Encode(resp)
}

func handle(req dispatch.Request, peer tether.Peer, log *slog.Logger) dispatch.Response {
	switch req.Action {
	case dispatch.ActionStatus:
		return dispatch.Response{OK: true, Delivered: peer.State() == tether.Open}

	case dispatch.ActionOpenURL:
		exec, err := dispatch.NewOpenURL(openURLArg(req))
		if err != nil {
			log.Info("request", "action", req.Action, "rejected", err)
			return dispatch.Response{Error: err.Error()}
		}
		args, _ := dispatch.ParseOpenURL(exec)

		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		defer cancel()
		err = peer.Send(ctx, exec)
		switch {
		case errors.Is(err, tether.ErrNotConnected):
			log.Info("request", "action", req.Action, "delivered", false, "id", exec.ID, "url", dispatch.RedactURL(args.URL))
			return dispatch.Response{OK: true, Error: "mac not connected"}
		case err != nil:
			log.Warn("request", "action", req.Action, "delivered", false, "id", exec.ID, "url", dispatch.RedactURL(args.URL), "err", err)
			return dispatch.Response{Error: "send failed"}
		}
		log.Info("request", "action", req.Action, "delivered", true, "id", exec.ID, "url", dispatch.RedactURL(args.URL))
		return dispatch.Response{OK: true, Delivered: true, ID: exec.ID}
	}
	log.Info("request", "action", "unknown")
	return dispatch.Response{Error: "unknown action"}
}

func openURLArg(req dispatch.Request) string {
	var a dispatch.OpenURLArgs
	json.Unmarshal(req.Args, &a)
	return a.URL
}
