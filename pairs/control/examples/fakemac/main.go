// Command fakemac stands in for the Mac half of the control plane, to try the
// dispatcher in a container: it bridges a unix socket to the VM gateway's
// listener, runs a Mac-side tether-gw on it, and serves ControlEvents and
// Forwards on the gateway's app socket, logging every call. It is not shipped.
//
// Port 9999 always fails with failed_precondition, to try the error path.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

func main() {
	var (
		gwBin     = flag.String("gw", "tether-gw", "tether-gw binary")
		pairDir   = flag.String("pair-dir", "", "control pair dir (manifest.json, api.binpb)")
		tokenFile = flag.String("token-file", "", "token file (mode 0600)")
		vmAddr    = flag.String("vm", "127.0.0.1:4100", "the VM gateway's listener")
		dir       = flag.String("dir", ".", "where the sockets go; fakemac runs in it, so keep the path short")
	)
	flag.Parse()
	if *pairDir == "" || *tokenFile == "" {
		fmt.Fprintln(os.Stderr, "fakemac: --pair-dir and --token-file are required")
		os.Exit(2)
	}
	if err := os.Chdir(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "fakemac:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	os.Remove("bridge.sock")
	ln, err := net.Listen("unix", "bridge.sock")
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakemac:", err)
		os.Exit(1)
	}
	go func() { <-ctx.Done(); ln.Close() }()
	go bridge(ln, *vmAddr)

	gw := exec.CommandContext(ctx, *gwBin, "--side", "mac", "--pair-dir", *pairDir, "--dial-unix", "bridge.sock",
		"--token-file", *tokenFile, "--gw-socket", "mac-gw.sock", "--app-socket", "mac-app.sock")
	gw.Stderr = os.Stderr
	if err := gw.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "fakemac:", err)
		os.Exit(1)
	}
	defer func() { gw.Process.Kill(); gw.Wait() }()

	abs, _ := filepath.Abs("mac-app.sock")
	logf("serving on %s", abs)
	if err := newMac().server().Serve(ctx, "mac-app.sock"); err != nil {
		fmt.Fprintln(os.Stderr, "fakemac:", err)
		os.Exit(1)
	}
}

func bridge(ln net.Listener, addr string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			d, err := net.Dial("tcp", addr)
			if err != nil {
				return
			}
			defer d.Close()
			go io.Copy(d, c)
			io.Copy(c, d)
		}()
	}
}

func logf(format string, args ...any) {
	fmt.Printf("%s fakemac: %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

type mac struct {
	mu   sync.Mutex
	fwds map[uint32]*controlv1.Forward
}

func newMac() *mac { return &mac{fwds: map[uint32]*controlv1.Forward{}} }

func who(ctx context.Context) string {
	if c, ok := tetherapp.CallerFrom(ctx); ok {
		return fmt.Sprintf("caller=%s uid=%d pid=%d", c.Side, c.UID, c.PID)
	}
	return "caller=none"
}

func (m *mac) server() *tetherapp.Server {
	s := tetherapp.NewServer()
	tetherapp.Handle(s, "/control.v1.ControlEvents/Health", func(ctx context.Context, _ *controlv1.HealthRequest) (*controlv1.HealthResponse, error) {
		logf("ControlEvents/Health %s", who(ctx))
		return &controlv1.HealthResponse{Side: "mac", Version: "fakemac", At: time.Now().UTC().Format(time.RFC3339Nano)}, nil
	})
	tetherapp.Handle(s, "/control.v1.ControlEvents/PairStatus", func(ctx context.Context, r *controlv1.PairStatusRequest) (*controlv1.PairStatusResponse, error) {
		logf("ControlEvents/PairStatus %s %v", who(ctx), r.Status)
		return &controlv1.PairStatusResponse{}, nil
	})
	tetherapp.Handle(s, "/control.v1.ControlEvents/PortsChanged", func(ctx context.Context, r *controlv1.PortsChangedRequest) (*controlv1.PortsChangedResponse, error) {
		var opened []string
		for _, l := range r.Opened {
			opened = append(opened, fmt.Sprintf("%d{addr=%s loopback_only=%v pid=%d process=%q cmd=%q}", l.Port, l.Address, l.LoopbackOnly, l.Pid, l.Process, l.Cmd))
		}
		logf("PortsChanged snapshot=%v opened=[%s] closed=%v %s", r.Snapshot, strings.Join(opened, " "), r.Closed, who(ctx))
		return &controlv1.PortsChangedResponse{}, nil
	})
	tetherapp.Handle(s, "/control.v1.Forwards/Expose", m.expose)
	tetherapp.Handle(s, "/control.v1.Forwards/Unexpose", m.unexpose)
	tetherapp.Handle(s, "/control.v1.Forwards/ListForwards", m.list)
	return s
}

func (m *mac) expose(ctx context.Context, r *controlv1.ExposeRequest) (*controlv1.Forward, error) {
	logf("Forwards/Expose port=%d target_host=%q label=%q requester=%v %s", r.Port, r.TargetHost, r.Label, r.Requester, who(ctx))
	if r.Port == 9999 {
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "devbox:9999 — port in use on the Mac")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.fwds[r.Port]; ok {
		return f, nil
	}
	if len(m.fwds) >= 32 {
		return nil, tetherapp.Errorf(tetherapp.CodeResourceExhausted, "too many forwards (32)")
	}
	f := &controlv1.Forward{
		VmPort: r.Port, MacPort: r.Port, Label: r.Label, Source: "expose", Requester: r.Requester,
		MacAddrs:  []string{fmt.Sprintf("devbox:%d", r.Port), fmt.Sprintf("localhost:%d", r.Port)},
		CreatedAt: time.Now().UTC().Format(time.RFC3339), State: "active",
	}
	m.fwds[r.Port] = f
	return f, nil
}

func (m *mac) unexpose(ctx context.Context, r *controlv1.UnexposeRequest) (*controlv1.UnexposeResponse, error) {
	logf("Forwards/Unexpose port=%d requester=%v %s", r.Port, r.Requester, who(ctx))
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.fwds, r.Port)
	return &controlv1.UnexposeResponse{}, nil
}

func (m *mac) list(ctx context.Context, _ *controlv1.ListForwardsRequest) (*controlv1.ListForwardsResponse, error) {
	logf("Forwards/ListForwards %s", who(ctx))
	m.mu.Lock()
	defer m.mu.Unlock()
	out := &controlv1.ListForwardsResponse{}
	for _, f := range m.fwds {
		out.Forwards = append(out.Forwards, f)
	}
	sort.Slice(out.Forwards, func(i, j int) bool { return out.Forwards[i].VmPort < out.Forwards[j].VmPort })
	return out, nil
}
