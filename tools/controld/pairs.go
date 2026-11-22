package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const (
	crashLimit     = 5
	crashWindow    = 5 * time.Minute
	controlDownMax = 30 * time.Second
)

var (
	pairNameRE  = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	pairTokenRE = regexp.MustCompile(`^[0-9a-fA-F]{32,128}$`)
	versionRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// packaged is the part of a pair's manifest.json controld reads.
type packaged struct {
	Pair    string `json:"pair"`
	Version string `json:"version"`
	VM      struct {
		App string `json:"app"`
	} `json:"vm"`
}

// runningPair is one pair's gateway and app, supervised.
type runningPair struct {
	name, requested, version, token string
	port                            uint32
	cancel                          context.CancelFunc
	wg                              sync.WaitGroup

	mu     sync.Mutex
	exits  []time.Time
	failed bool
}

// pairMgr starts and stops the VM side of pairs on the control plane's say-so.
type pairMgr struct {
	log      *slog.Logger
	cfg      *platform.Config
	host     string
	gwBin    string
	root     string // installed pairs
	state    string // $BOX_TETHER_STATE_DIR
	control  string // control gateway socket, for PairStatus
	problems *problems
	ctx      context.Context

	apply sync.Mutex // one ApplyPairs at a time
	mu    sync.Mutex
	pairs map[string]*runningPair
	last  map[string]*controlv1.PairResult // latest state of each pair this session, for status
}

func (m *pairMgr) pairState(name string) string { return filepath.Join(m.state, name) }

// ApplyPairs makes the running set equal to the request.
func (m *pairMgr) ApplyPairs(_ context.Context, req *controlv1.ApplyPairsRequest) (*controlv1.ApplyPairsResponse, error) {
	m.apply.Lock()
	defer m.apply.Unlock()

	results := make([]*controlv1.PairResult, len(req.Pairs))
	want := map[string]bool{}
	ports := map[uint32]bool{}
	var valid []int
	for i, s := range req.Pairs {
		r := &controlv1.PairResult{Name: s.Name, VmPort: s.VmPort, Version: s.Version}
		results[i] = r
		switch {
		case !pairNameRE.MatchString(s.Name):
			r.State, r.Error = "failed", "invalid pair name"
		case s.Name == "control":
			r.State, r.Error = "failed", "control is not an applied pair"
		case want[s.Name]:
			r.State, r.Error = "failed", "pair listed twice"
		case int(s.VmPort) < m.cfg.PairPortMin || int(s.VmPort) > m.cfg.PairPortMax:
			r.State, r.Error = "failed", fmt.Sprintf("vm_port must be %d-%d", m.cfg.PairPortMin, m.cfg.PairPortMax)
		case ports[s.VmPort]:
			r.State, r.Error = "failed", "vm_port used by another pair in the request"
		case !pairTokenRE.MatchString(s.Token):
			r.State, r.Error = "failed", "token must be 32-128 hex characters"
		case s.Version != "latest" && !versionRE.MatchString(s.Version):
			r.State, r.Error = "failed", "invalid version"
		default:
			want[s.Name] = true
			ports[s.VmPort] = true
			valid = append(valid, i)
			continue
		}
		m.log.Info("pair rejected", "pair", s.Name, "port", s.VmPort, "state", r.State, "err", r.Error)
	}

	// Stop what is no longer wanted or has changed.
	m.mu.Lock()
	var stale []*runningPair
	for name, p := range m.pairs {
		keep := false
		for _, i := range valid {
			s := req.Pairs[i]
			if s.Name == name && s.Version == p.requested && s.Token == p.token && s.VmPort == p.port && !p.isFailed() {
				keep = true
			}
		}
		if !keep {
			stale = append(stale, p)
			delete(m.pairs, name)
		}
	}
	m.mu.Unlock()
	for _, p := range stale {
		m.stop(p, "not in the applied set")
	}

	for _, i := range valid {
		s, r := req.Pairs[i], results[i]
		m.mu.Lock()
		p := m.pairs[s.Name]
		m.mu.Unlock()
		if p != nil {
			r.State, r.Host, r.Version = "started", m.host, p.version
			continue
		}
		dir, man, err := m.locate(s.Name, s.Version)
		switch {
		case errors.Is(err, os.ErrNotExist):
			r.State, r.Error = "not_installed", "pair is not installed on the vm"
		case err != nil:
			r.State, r.Error = "failed", err.Error()
			if errors.Is(err, errMismatch) {
				r.State = "mismatch"
			}
		default:
			r.Version = man.Version
			if err := m.start(s, dir, man); err != nil {
				r.State, r.Error = "failed", err.Error()
			} else {
				r.State, r.Host = "started", m.host
			}
		}
		m.log.Info("pair", "pair", s.Name, "version", r.Version, "port", s.VmPort, "state", r.State, "err", r.Error)
	}
	m.mu.Lock()
	m.last = map[string]*controlv1.PairResult{}
	for _, r := range results {
		if r.State != "started" {
			m.problems.add("pair/"+r.Name, "%s: %s", r.State, r.Error)
		}
		m.last[r.Name] = r
	}
	m.mu.Unlock()
	return &controlv1.ApplyPairsResponse{Results: results}, nil
}

var errMismatch = errors.New("installed pair does not match")

// locate finds an installed pair and reads its manifest; a missing pair wraps os.ErrNotExist.
func (m *pairMgr) locate(name, version string) (string, packaged, error) {
	var man packaged
	if version == "latest" {
		version = "current"
	}
	dir, err := filepath.EvalSymlinks(filepath.Join(m.root, name, version))
	if err != nil {
		return "", man, err
	}
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", man, err
	}
	if _, err := os.Stat(filepath.Join(dir, "api.binpb")); err != nil {
		return "", man, err
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		return "", man, fmt.Errorf("manifest.json: %w", err)
	}
	if man.Pair != name {
		return "", man, fmt.Errorf("%w: %s holds pair %q", errMismatch, dir, man.Pair)
	}
	if app := man.VM.App; app != "" {
		if filepath.Base(app) != app {
			return "", man, fmt.Errorf("manifest vm.app %q is not a plain file name", app)
		}
		if _, err := os.Stat(filepath.Join(dir, app)); err != nil {
			return "", man, fmt.Errorf("vm app: %w", err)
		}
	}
	return dir, man, nil
}

func (m *pairMgr) start(s *controlv1.PairSpec, dir string, man packaged) error {
	if ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", s.VmPort)); err != nil {
		return fmt.Errorf("port %d is not available", s.VmPort)
	} else {
		ln.Close()
	}
	sd := m.pairState(s.Name)
	if err := os.MkdirAll(sd, 0o700); err != nil {
		return err
	}
	tokenFile := filepath.Join(sd, "token")
	if err := writeSecret(tokenFile, s.Token); err != nil {
		return err
	}
	gwSock, appSock := filepath.Join(sd, "gw.sock"), filepath.Join(sd, "app.sock")

	ctx, cancel := context.WithCancel(m.ctx)
	p := &runningPair{name: s.Name, requested: s.Version, version: man.Version, token: s.Token, port: s.VmPort, cancel: cancel}
	lg := m.log.With("pair", s.Name, "version", man.Version, "port", s.VmPort)
	onExit := func() bool { return m.recordExit(p) }

	gw := &supervised{
		log: lg.With("proc", "gw"), logf: filepath.Join(sd, "gw.log"), onExit: onExit, env: childEnv(),
		argv: []string{m.gwBin, "--side", "vm", "--pair-dir", dir, "--listen", fmt.Sprintf("0.0.0.0:%d", s.VmPort),
			"--token-file", tokenFile, "--gw-socket", gwSock, "--app-socket", appSock},
	}
	p.wg.Go(func() { gw.run(ctx) })
	if app := man.VM.App; app != "" {
		a := &supervised{
			log: lg.With("proc", "app"), logf: filepath.Join(sd, "app.log"), onExit: onExit,
			env:  childEnv("TETHER_GW_SOCKET="+gwSock, "TETHER_APP_SOCKET="+appSock),
			argv: []string{filepath.Join(dir, app)},
		}
		p.wg.Go(func() { a.run(ctx) })
	}
	m.mu.Lock()
	m.pairs[s.Name] = p
	m.mu.Unlock()
	return nil
}

func writeSecret(path, secret string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(secret), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p *runningPair) isFailed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed
}

// recordExit counts an exit of the pair's gateway or app, and gives up on the
// pair after crashLimit exits within crashWindow.
func (m *pairMgr) recordExit(p *runningPair) bool {
	now := time.Now()
	p.mu.Lock()
	keep := p.exits[:0]
	for _, t := range p.exits {
		if now.Sub(t) < crashWindow {
			keep = append(keep, t)
		}
	}
	p.exits = append(keep, now)
	loop := len(p.exits) >= crashLimit
	if loop {
		p.failed = true
	}
	p.mu.Unlock()
	if loop {
		msg := fmt.Sprintf("crashed %d times in %s; stopped", crashLimit, crashWindow)
		go m.giveUp(p, msg)
		return false
	}
	return true
}

// giveUp stops a crash-looping pair and tells the Mac.
func (m *pairMgr) giveUp(p *runningPair, msg string) {
	p.cancel()
	m.log.Error("pair failed", "pair", p.name, "version", p.version, "port", p.port, "state", "failed", "err", msg)
	r := &controlv1.PairResult{Name: p.name, State: "failed", Error: msg, Host: m.host, VmPort: p.port, Version: p.version}
	m.mu.Lock()
	m.last[p.name] = r
	m.mu.Unlock()
	m.problems.add("pair/"+p.name, "%s", msg)
	m.report(r)
}

// results returns the latest state of each pair applied this session, by name.
func (m *pairMgr) results() []*controlv1.PairResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*controlv1.PairResult, 0, len(m.last))
	for _, name := range slices.Sorted(maps.Keys(m.last)) {
		out = append(out, m.last[name])
	}
	return out
}

// report sends PairStatus over the control gateway.
func (m *pairMgr) report(r *controlv1.PairResult) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := tetherapp.Call(ctx, m.control, "/control.v1.ControlEvents/PairStatus", &controlv1.PairStatusRequest{Status: r}, &controlv1.PairStatusResponse{})
	if err != nil {
		m.log.Warn("PairStatus not delivered", "pair", r.Name, "err", err)
	}
}

func (m *pairMgr) stop(p *runningPair, why string) {
	p.cancel()
	p.wg.Wait()
	sd := m.pairState(p.name)
	os.Remove(filepath.Join(sd, "gw.sock"))
	os.Remove(filepath.Join(sd, "app.sock"))
	os.Remove(filepath.Join(sd, "token"))
	m.log.Info("pair", "pair", p.name, "version", p.version, "port", p.port, "state", "stopped", "why", why)
}

func (m *pairMgr) stopAll(why string) {
	m.apply.Lock()
	defer m.apply.Unlock()
	m.mu.Lock()
	all := make([]*runningPair, 0, len(m.pairs))
	for _, p := range m.pairs {
		all = append(all, p)
	}
	m.pairs = map[string]*runningPair{}
	m.mu.Unlock()
	for _, p := range all {
		m.stop(p, why)
	}
	m.mu.Lock()
	clear(m.last)
	m.mu.Unlock()
}

func (m *pairMgr) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pairs)
}

// watchControl stops every pair once the control connection has been down for
// controlDownMax: tethers only exist while the user's session asks for them.
func (m *pairMgr) watchControl(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var downSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if gwConnected(ctx, m.control) {
			downSince = time.Time{}
			continue
		}
		if downSince.IsZero() {
			downSince = time.Now()
		}
		if time.Since(downSince) > controlDownMax && m.count() > 0 {
			m.log.Warn("control connection is down; stopping all pairs", "down_for", time.Since(downSince).Round(time.Second))
			m.stopAll("control connection down")
		}
	}
}

// gwConnected asks a gateway's /healthz whether it has a live peer.
func gwConnected(ctx context.Context, socket string) bool {
	hc := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
		DisableKeepAlives: true,
	}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://gw/healthz", nil)
	resp, err := hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		Connected bool `json:"connected"`
	}
	return json.NewDecoder(resp.Body).Decode(&h) == nil && h.Connected
}

// findBin looks for a tool on PATH, then in the sync-owned bin directory.
func findBin(cfg *platform.Config, name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	p := filepath.Join(cfg.DevtoolsDir, "bin", name)
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}
