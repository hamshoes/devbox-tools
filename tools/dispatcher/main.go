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
	"unicode/utf8"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tether"
)

var version = "dev"

const (
	maxRequest   = 4 << 20 // a clip of 512 KiB can escape to several times its size
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

	cfg, err := platform.Load()
	if err != nil {
		return err
	}
	token := loadToken(cfg)
	if token == "" {
		return fmt.Errorf("no token: set %s in the environment or in %s", dispatch.TokenEnv, filepath.Join(cfg.ConfigDir, "env"))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dispatcherPort := dispatch.DispatcherPort
	fwd := newForwards(log)
	router := tether.NewRouter(log)
	tether.On(router, fwd.onReady)
	tether.On(router, fwd.onFailed)
	peer, err := tether.Listen(ctx, tether.ServerConfig{
		Addr:  fmt.Sprintf("0.0.0.0:%d", dispatcherPort),
		Path:  dispatch.TetherPath,
		Token: token,
	}, router, log)
	if err != nil {
		return err
	}
	defer peer.Close()
	fwd.peer = peer
	peer.OnState(func(s tether.State) { log.Info("tether", "state", s) })
	peer.OnState(fwd.onState)
	go fwd.watch(ctx)

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
	log.Info("listening", "port", dispatcherPort, "socket", sock)
	cp, stopControl := startControl(ctx, log, cfg, peer, fwd.hostIP, token)
	defer stopControl()

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
		go serve(conn, peer, fwd, cp, log)
	}
}

// loadToken reads the token from the environment, then from the env file in the devbox config directory.
func loadToken(cfg *platform.Config) string {
	if t := os.Getenv(dispatch.TokenEnv); t != "" {
		return t
	}
	f, err := os.Open(filepath.Join(cfg.ConfigDir, "env"))
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

func serve(conn net.Conn, peer tether.Peer, fwd *forwards, cp *controlPlane, log *slog.Logger) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(readDeadline))
	var req dispatch.Request
	if err := json.NewDecoder(io.LimitReader(conn, maxRequest)).Decode(&req); err != nil {
		reply(conn, dispatch.Response{Error: "bad request"})
		return
	}
	// expose can block for replyWait before it answers.
	conn.SetDeadline(time.Now().Add(replyWait + readDeadline))
	resp := handle(req, requesterOf(conn), peer, fwd, cp, log)
	reply(conn, resp)
}

func reply(conn net.Conn, resp dispatch.Response) {
	json.NewEncoder(conn).Encode(resp)
}

func handle(req dispatch.Request, rq *dispatch.Requester, peer tether.Peer, fwd *forwards, cp *controlPlane, log *slog.Logger) dispatch.Response {
	ctx := context.Background()
	switch req.Action {
	case dispatch.ActionExpose:
		var a dispatch.ExposeArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return dispatch.Response{Error: "bad args"}
		}
		if cp.up(ctx) {
			res, err := cp.expose(ctx, a, rq)
			if err == nil {
				log.Info("request", "action", req.Action, "via", "control", "port", a.Port, "requester_pid", rqPID(rq))
				return resultResponse(res)
			}
			if !controlUnavailable(err) {
				log.Info("request", "action", req.Action, "via", "control", "rejected", err)
				return dispatch.Response{Error: cp.controlMessage(err)}
			}
		}
		res, err := fwd.expose(a)
		if err != nil {
			log.Info("request", "action", req.Action, "rejected", err)
			return dispatch.Response{Error: friendly(err), Legacy: true}
		}
		resp := resultResponse(res)
		resp.Legacy = true
		return resp

	case dispatch.ActionUnexpose:
		var a dispatch.UnexposeArgs
		if err := json.Unmarshal(req.Args, &a); err != nil || (a.Name == "" && a.Port == 0) {
			return dispatch.Response{Error: "bad args"}
		}
		if cp.up(ctx) {
			if a.Port == 0 {
				return dispatch.Response{Error: "unexpose takes a port"}
			}
			err := cp.unexpose(ctx, a.Port, rq)
			if err == nil {
				log.Info("request", "action", req.Action, "via", "control", "port", a.Port, "requester_pid", rqPID(rq))
				return resultResponse(struct{}{})
			}
			if !controlUnavailable(err) {
				return dispatch.Response{Error: cp.controlMessage(err)}
			}
		}
		if err := fwd.unexpose(a); err != nil {
			return dispatch.Response{Error: err.Error(), Legacy: true}
		}
		resp := resultResponse(struct{}{})
		resp.Legacy = true
		return resp

	case dispatch.ActionPorts:
		return resultResponse(fwd.ports())

	case dispatch.ActionListForwards:
		if !cp.up(ctx) {
			return dispatch.Response{OK: true, Error: dispatch.ErrControlDown}
		}
		list, err := cp.list(ctx)
		if err != nil {
			return dispatch.Response{Error: cp.controlMessage(err)}
		}
		return resultResponse(list)

	case dispatch.ActionStatus:
		return dispatch.Response{OK: true, Delivered: peer.State() == tether.Open}

	case dispatch.ActionOpenURL:
		exec, err := dispatch.NewOpenURL(openURLArg(req))
		if err != nil {
			log.Info("request", "action", req.Action, "rejected", err)
			return dispatch.Response{Error: err.Error()}
		}
		args, _ := dispatch.ParseOpenURL(exec)
		return deliver(req.Action, exec, peer, log, "url", dispatch.RedactURL(args.URL))

	case dispatch.ActionNotify:
		var a dispatch.NotifyArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return dispatch.Response{Error: "bad args"}
		}
		exec, err := dispatch.NewNotify(a)
		if err != nil {
			log.Info("request", "action", req.Action, "rejected", err)
			return dispatch.Response{Error: err.Error()}
		}
		a, _ = dispatch.ParseNotify(exec)
		// Message content is never logged, only its length.
		attrs := []any{"title", a.Title, "message_runes", utf8.RuneCountInString(a.Message)}
		if a.URL != "" {
			attrs = append(attrs, "url", dispatch.RedactURL(a.URL))
		}
		return deliver(req.Action, exec, peer, log, attrs...)

	case dispatch.ActionClip:
		var a dispatch.ClipArgs
		if err := json.Unmarshal(req.Args, &a); err != nil {
			return dispatch.Response{Error: "bad args"}
		}
		exec, err := dispatch.NewClip(a.Text)
		if err != nil {
			log.Info("request", "action", req.Action, "rejected", err)
			return dispatch.Response{Error: err.Error()}
		}
		// Clipboard content is never logged, only its size.
		return deliver(req.Action, exec, peer, log, "bytes", len(a.Text))
	}
	log.Info("request", "action", "unknown")
	return dispatch.Response{Error: "unknown action"}
}

func rqPID(rq *dispatch.Requester) int32 {
	if rq == nil {
		return 0
	}
	return rq.PID
}

func openURLArg(req dispatch.Request) string {
	var a dispatch.OpenURLArgs
	json.Unmarshal(req.Args, &a)
	return a.URL
}

func resultResponse(v any) dispatch.Response {
	data, err := json.Marshal(v)
	if err != nil {
		return dispatch.Response{Error: "internal error"}
	}
	return dispatch.Response{OK: true, Result: data}
}

// deliver sends exec to the Mac and builds the response; attrs are extra log fields.
func deliver(action string, exec dispatch.Exec, peer tether.Peer, log *slog.Logger, attrs ...any) dispatch.Response {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	err := peer.Send(ctx, exec)
	base := append([]any{"action", action, "id", exec.ID}, attrs...)
	switch {
	case errors.Is(err, tether.ErrNotConnected):
		log.Info("request", append(base, "delivered", false)...)
		return dispatch.Response{OK: true, Error: "mac not connected"}
	case err != nil:
		log.Warn("request", append(base, "delivered", false, "err", err)...)
		return dispatch.Response{Error: "send failed"}
	}
	log.Info("request", append(base, "delivered", true)...)
	return dispatch.Response{OK: true, Delivered: true, ID: exec.ID}
}
