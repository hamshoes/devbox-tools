package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

// Service sources, from strongest to weakest: dev config wins for the names
// it lists; manifest autostart and bb svc fill in the rest and persist.
const (
	sourceConfig   = "config"
	sourceManifest = "manifest"
	sourceBB       = "bb"
)

// Service states, as reported to dev and bb.
const (
	stateRunning      = "running"
	stateDisabled     = "disabled"
	stateNotInstalled = "not_installed"
	stateFailed       = "failed"
)

// svcEntry is one service in services.json.
type svcEntry struct {
	Tool    string            `json:"tool"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	Enabled bool              `json:"enabled"`
	Source  string            `json:"source"`
}

func (e *svcEntry) same(o *svcEntry) bool {
	return e.Tool == o.Tool && slices.Equal(e.Args, o.Args) && maps.Equal(e.Env, o.Env)
}

type svcFile struct {
	Version  int                  `json:"version"`
	Services map[string]*svcEntry `json:"services"`
}

// manifestTool is the part of the cached manifest.json controld reads.
type manifestTool struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Service *struct {
		Args      []string `json:"args"`
		Autostart bool     `json:"autostart"`
	} `json:"service"`
}

// runningSvc is one supervised service process.
type runningSvc struct {
	entry  svcEntry
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	exits []time.Time
}

type svcStatus struct {
	state, err string
	since      time.Time
}

// serviceMgr owns services.json and keeps the running services equal to it.
// Services run for controld's lifetime, not a session's.
type serviceMgr struct {
	log      *slog.Logger
	cfg      *platform.Config
	problems *problems
	ctx      context.Context
	path     string // services.json

	mu      sync.Mutex
	entries map[string]*svcEntry
	running map[string]*runningSvc
	status  map[string]svcStatus
	wg      sync.WaitGroup
}

func newServiceMgr(ctx context.Context, log *slog.Logger, cfg *platform.Config, pr *problems) *serviceMgr {
	return &serviceMgr{
		log: log, cfg: cfg, problems: pr, ctx: ctx,
		path:    filepath.Join(cfg.ConfigDir, "services.json"),
		entries: map[string]*svcEntry{}, running: map[string]*runningSvc{}, status: map[string]svcStatus{},
	}
}

// start loads services.json, merges the manifest layer and starts what is enabled.
func (m *serviceMgr) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, err := os.ReadFile(m.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		m.problems.add("services", "read %s: %v", m.path, err)
	default:
		var f svcFile
		if err := json.Unmarshal(raw, &f); err != nil || f.Version != 1 {
			m.problems.add("services", "%s is not a version 1 services file; starting from the manifest only", m.path)
		} else {
			for name, e := range f.Services {
				if pairNameRE.MatchString(name) && e != nil {
					m.entries[name] = e
				}
			}
		}
	}
	m.reconcileLocked()
}

// stopAll stops every service and waits; used when controld exits.
func (m *serviceMgr) stopAll() {
	m.mu.Lock()
	for name, r := range m.running {
		r.cancel()
		delete(m.running, name)
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// readManifest returns the tools in the manifest copy sync caches. A missing
// copy is an empty manifest: nothing counts as installed until sync has run.
func (m *serviceMgr) readManifest() map[string]manifestTool {
	path := filepath.Join(m.cfg.DevtoolsDir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var man struct {
		Tools []manifestTool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &man); err != nil {
		m.problems.add("services", "%s: %v", path, err)
		return nil
	}
	out := map[string]manifestTool{}
	for _, t := range man.Tools {
		if t.Kind == "" || t.Kind == "tool" {
			out[t.Name] = t
		}
	}
	return out
}

// mergeManifestLocked adds manifest autostart services nobody else named,
// refreshes the args of those already taken from it, and drops the ones the
// manifest no longer marks as autostart.
func (m *serviceMgr) mergeManifestLocked(tools map[string]manifestTool) {
	for name, e := range m.entries {
		if e.Source != sourceManifest {
			continue
		}
		t, ok := tools[name]
		if !ok || t.Service == nil || !t.Service.Autostart {
			delete(m.entries, name)
			continue
		}
		e.Tool, e.Args = name, t.Service.Args
	}
	for name, t := range tools {
		if _, taken := m.entries[name]; !taken && t.Service != nil && t.Service.Autostart && pairNameRE.MatchString(name) {
			m.entries[name] = &svcEntry{Tool: name, Args: t.Service.Args, Enabled: true, Source: sourceManifest}
		}
	}
}

// reconcileLocked makes the running set match the entries, records each
// service's state and saves services.json.
func (m *serviceMgr) reconcileLocked() {
	tools := m.readManifest()
	m.mergeManifestLocked(tools)

	var stopped []*runningSvc
	for name, r := range m.running {
		e, ok := m.entries[name]
		if !ok || !e.Enabled || !e.same(&r.entry) {
			r.cancel()
			delete(m.running, name)
			stopped = append(stopped, r)
			m.log.Info("service stopped", "service", name)
		}
	}
	// A changed service must not overlap with its old process. The wait is
	// bounded by the supervisor's SIGKILL delay.
	for _, r := range stopped {
		<-r.done
	}
	for name, e := range m.entries {
		if !e.Enabled {
			m.setLocked(name, stateDisabled, "")
			continue
		}
		bin := ""
		if _, listed := tools[e.Tool]; listed {
			bin = findBin(m.cfg, e.Tool)
		}
		if bin == "" {
			msg := fmt.Sprintf("tool %q is not in the manifest or not installed", e.Tool)
			if m.status[name].state != stateNotInstalled {
				m.problems.add("service/"+name, "%s", msg)
			}
			m.setLocked(name, stateNotInstalled, msg)
			continue
		}
		if _, ok := m.running[name]; ok {
			continue
		}
		m.startLocked(name, e, bin)
	}
	for name := range m.status {
		if _, ok := m.entries[name]; !ok {
			delete(m.status, name)
		}
	}
	if err := m.saveLocked(); err != nil {
		m.problems.add("services", "save %s: %v", m.path, err)
	}
}

func (m *serviceMgr) setLocked(name, state, msg string) {
	if s := m.status[name]; s.state == state && s.err == msg {
		return
	}
	m.status[name] = svcStatus{state: state, err: msg, since: time.Now()}
}

func (m *serviceMgr) startLocked(name string, e *svcEntry, bin string) {
	logDir := filepath.Join(m.cfg.StateDir, "services")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		m.setLocked(name, stateFailed, err.Error())
		return
	}
	env := make([]string, 0, len(e.Env))
	for _, k := range slices.Sorted(maps.Keys(e.Env)) {
		env = append(env, k+"="+e.Env[k])
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &runningSvc{entry: *e, cancel: cancel, done: make(chan struct{})}
	lg := m.log.With("service", name, "tool", e.Tool)
	sv := &supervised{
		log: lg, logf: filepath.Join(logDir, name+".log"), env: childEnv(env...),
		argv:   append([]string{bin}, e.Args...),
		onExit: func() bool { return m.recordExit(name, r) },
	}
	m.running[name] = r
	m.setLocked(name, stateRunning, "")
	m.wg.Go(func() {
		defer close(r.done)
		sv.run(ctx)
	})
	lg.Info("service started", "args", e.Args)
}

// recordExit gives up on a service after crashLimit exits within crashWindow.
func (m *serviceMgr) recordExit(name string, r *runningSvc) bool {
	now := time.Now()
	r.mu.Lock()
	r.exits = slices.DeleteFunc(r.exits, func(t time.Time) bool { return now.Sub(t) >= crashWindow })
	r.exits = append(r.exits, now)
	loop := len(r.exits) >= crashLimit
	r.mu.Unlock()
	if !loop {
		return true
	}
	msg := fmt.Sprintf("crashed %d times in %s; stopped", crashLimit, crashWindow)
	go func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.running[name] == r {
			delete(m.running, name)
			m.setLocked(name, stateFailed, msg)
		}
	}()
	m.problems.add("service/"+name, "%s", msg)
	return false
}

func (m *serviceMgr) saveLocked() error {
	raw, err := json.MarshalIndent(svcFile{Version: 1, Services: m.entries}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func (m *serviceMgr) statusLocked(name string) *controlv1.ServiceStatus {
	st := &controlv1.ServiceStatus{Name: name, State: stateNotInstalled}
	if e, ok := m.entries[name]; ok {
		st.Tool, st.Args, st.Env, st.Enabled, st.Source = e.Tool, e.Args, e.Env, e.Enabled, e.Source
	}
	if s, ok := m.status[name]; ok {
		st.State, st.Error, st.Since = s.state, s.err, s.since.UTC().Format(time.RFC3339)
	}
	return st
}

// list returns every service, by name.
func (m *serviceMgr) list() []*controlv1.ServiceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*controlv1.ServiceStatus, 0, len(m.entries))
	for _, name := range slices.Sorted(maps.Keys(m.entries)) {
		out = append(out, m.statusLocked(name))
	}
	return out
}

// ApplyServices replaces the config layer with the request (Control/ApplyServices).
func (m *serviceMgr) ApplyServices(_ context.Context, req *controlv1.ApplyServicesRequest) (*controlv1.ApplyServicesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resp := &controlv1.ApplyServicesResponse{}
	var bad []*controlv1.ServiceStatus
	seen := map[string]bool{}
	for name, e := range m.entries {
		if e.Source == sourceConfig {
			delete(m.entries, name)
		}
	}
	for _, s := range req.Services {
		switch {
		case !pairNameRE.MatchString(s.Name):
			bad = append(bad, &controlv1.ServiceStatus{Name: s.Name, State: stateFailed, Error: "invalid service name"})
		case seen[s.Name]:
			bad = append(bad, &controlv1.ServiceStatus{Name: s.Name, State: stateFailed, Error: "service listed twice"})
		case s.Tool == "":
			bad = append(bad, &controlv1.ServiceStatus{Name: s.Name, State: stateFailed, Error: "no tool"})
		default:
			seen[s.Name] = true
			m.entries[s.Name] = &svcEntry{Tool: s.Tool, Args: s.Args, Env: s.Env, Enabled: s.Enabled, Source: sourceConfig}
		}
	}
	m.reconcileLocked()
	for _, s := range req.Services {
		if seen[s.Name] {
			resp.Services = append(resp.Services, m.statusLocked(s.Name))
		}
	}
	resp.Services = append(resp.Services, bad...)
	for _, s := range resp.Services {
		m.log.Info("service", "service", s.Name, "source", s.Source, "state", s.State, "err", s.Error)
	}
	return resp, nil
}

// setEnabled is bb svc enable/disable. A service dev config names can't be
// changed here; a new one must be a manifest tool.
func (m *serviceMgr) setEnabled(name string, enabled bool) (*controlv1.ServiceStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[name]
	switch {
	case ok && e.Source == sourceConfig:
		return nil, tetherapp.Errorf(tetherapp.CodeFailedPrecondition, "%s is set in dev config; change it there", name)
	case ok:
		e.Enabled, e.Source = enabled, sourceBB
	case !enabled:
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "no service %q", name)
	case !pairNameRE.MatchString(name):
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "invalid service name %q", name)
	default:
		t, listed := m.readManifest()[name]
		if !listed {
			return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "no tool %q in the manifest", name)
		}
		args := []string{}
		if t.Service != nil {
			args = t.Service.Args
		}
		m.entries[name] = &svcEntry{Tool: name, Args: args, Enabled: true, Source: sourceBB}
	}
	m.reconcileLocked()
	return m.statusLocked(name), nil
}
