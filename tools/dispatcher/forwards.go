package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
	"github.com/hamshoes/devbox-tools/pkg/tether"
)

const (
	replyWait     = 10 * time.Second
	watchInterval = 3 * time.Second
	watchMisses   = 3
)

type forward struct {
	Name       string    `json:"name"`
	Host       string    `json:"host"`
	VMPort     int       `json:"vm_port"`
	MacPort    int       `json:"mac_port"`
	State      string    `json:"state"`
	Open       bool      `json:"open"`
	Opened     bool      `json:"opened"`
	LastExecID string    `json:"last_exec_id"`
	Error      string    `json:"error"`
	Since      time.Time `json:"since"`
	misses     int
}

type waitResult struct{ ok bool }

// forwards is the registry of exposed ports. Every method that takes mu is safe
// to call from the tether handler goroutines.
type forwards struct {
	log  *slog.Logger
	peer tether.Peer
	path string

	mu      sync.Mutex
	m       map[string]*forward
	ip      string
	waiters map[string]chan waitResult // by exec ID, for CLIs blocked in expose
}

func statePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/dispatch/forwards.json")
}

func newForwards(log *slog.Logger) *forwards {
	f := &forwards{log: log, path: statePath(), m: map[string]*forward{}, waiters: map[string]chan waitResult{}}
	if ip, err := containerIP(); err != nil {
		log.Warn("container ip", "err", err)
	} else {
		f.ip = ip
		log.Info("container ip", "ip", ip)
	}
	f.load()
	return f
}

// load restores the registry with every entry waiting: nothing is known to be forwarded after a restart.
func (f *forwards) load() {
	data, err := os.ReadFile(f.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			f.log.Warn("load forwards", "err", err)
		}
		return
	}
	var list []*forward
	if err := json.Unmarshal(data, &list); err != nil {
		f.log.Warn("load forwards", "err", err)
		return
	}
	for _, e := range list {
		if dispatch.ValidateForwardName(e.Name) != nil || dispatch.ValidateForwardPort(e.VMPort) != nil || len(f.m) >= dispatch.MaxForwards {
			continue
		}
		e.State = dispatch.StateWaiting
		e.Error = ""
		e.Since = time.Now()
		f.m[e.Name] = e
	}
	f.log.Info("loaded forwards", "count", len(f.m))
}

// saveLocked writes the registry to a temp file and renames it into place.
func (f *forwards) saveLocked() {
	list := make([]*forward, 0, len(f.m))
	for _, e := range f.m {
		list = append(list, e)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	data, err := json.MarshalIndent(list, "", "  ")
	if err == nil {
		err = os.MkdirAll(filepath.Dir(f.path), 0o700)
	}
	if err == nil {
		tmp := f.path + ".tmp"
		if err = os.WriteFile(tmp, data, 0o600); err == nil {
			err = os.Rename(tmp, f.path)
		}
	}
	if err != nil {
		f.log.Warn("save forwards", "err", err)
	}
}

func (f *forwards) setStateLocked(e *forward, s string) {
	if e.State == s {
		return
	}
	f.log.Info("forward", "name", e.Name, "vm_port", e.VMPort, "mac_port", e.MacPort, "from", e.State, "to", s)
	e.State = s
	e.Since = time.Now()
}

func (f *forwards) connected() bool { return f.peer.State() == tether.Open }

func (f *forwards) sendCtx(m tether.Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	return f.peer.Send(ctx, m)
}

// sendLocked sends forward_port for e with a new exec ID and registers a waiter for the reply.
// If the Mac turns out not to be connected, e becomes waiting.
func (f *forwards) sendLocked(e *forward) (chan waitResult, string, error) {
	if f.ip == "" {
		ip, err := containerIP()
		if err != nil {
			return nil, "", err
		}
		f.ip = ip
	}
	exec, err := dispatch.NewForwardPort(e.Name, f.ip, e.VMPort)
	if err != nil {
		return nil, "", err
	}
	delete(f.waiters, e.LastExecID)
	e.LastExecID, e.Host = exec.ID, f.ip
	ch := make(chan waitResult, 1)
	f.waiters[exec.ID] = ch
	if err := f.sendCtx(exec); err != nil {
		delete(f.waiters, exec.ID)
		if errors.Is(err, tether.ErrNotConnected) {
			f.setStateLocked(e, dispatch.StateWaiting)
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("send failed: %w", err)
	}
	return ch, exec.ID, nil
}

func (f *forwards) openLocked(e *forward) {
	if !e.Open || e.Opened {
		return
	}
	exec, err := dispatch.NewOpenURL(fmt.Sprintf("http://localhost:%d", e.MacPort))
	if err == nil {
		err = f.sendCtx(exec)
	}
	if err != nil {
		f.log.Warn("forward open", "name", e.Name, "err", err)
		return
	}
	e.Opened = true
}

func (f *forwards) cancelLocked(e *forward) {
	if (e.State != dispatch.StateActive && e.State != dispatch.StatePending) || !f.connected() {
		return
	}
	exec, err := dispatch.NewForwardCancel(e.Name)
	if err == nil {
		err = f.sendCtx(exec)
	}
	if err != nil {
		f.log.Warn("forward cancel", "name", e.Name, "err", err)
	}
}

func resultOf(e *forward) dispatch.ExposeResult {
	return dispatch.ExposeResult{Name: e.Name, VMPort: e.VMPort, MacPort: e.MacPort, State: e.State}
}

func (f *forwards) expose(a dispatch.ExposeArgs) (dispatch.ExposeResult, error) {
	if a.Name == "" {
		a.Name = fmt.Sprintf("p%d", a.Port)
	}
	if err := dispatch.ValidateForwardPort(a.Port); err != nil {
		return dispatch.ExposeResult{}, err
	}
	if err := dispatch.ValidateForwardName(a.Name); err != nil {
		return dispatch.ExposeResult{}, err
	}
	ports, err := listenPorts()
	if err != nil {
		return dispatch.ExposeResult{}, err
	}
	switch reachable, ok := ports[a.Port]; {
	case !ok:
		return dispatch.ExposeResult{}, fmt.Errorf("nothing listening on :%d", a.Port)
	case !reachable:
		return dispatch.ExposeResult{}, fmt.Errorf("service listens on loopback; bind 0.0.0.0:%d", a.Port)
	}

	f.mu.Lock()
	for _, o := range f.m {
		if o.Name != a.Name && o.VMPort == a.Port {
			f.mu.Unlock()
			return dispatch.ExposeResult{}, fmt.Errorf("port %d is already exposed as %s", a.Port, o.Name)
		}
	}
	e := f.m[a.Name]
	switch {
	case e != nil && e.VMPort != a.Port:
		f.mu.Unlock()
		return dispatch.ExposeResult{}, fmt.Errorf("name %s already exposes port %d", a.Name, e.VMPort)
	case e == nil && len(f.m) >= dispatch.MaxForwards:
		f.mu.Unlock()
		return dispatch.ExposeResult{}, fmt.Errorf("too many forwards (max %d)", dispatch.MaxForwards)
	case e == nil:
		e = &forward{Name: a.Name, VMPort: a.Port, State: dispatch.StateWaiting, Since: time.Now()}
		f.m[e.Name] = e
	}
	e.misses = 0
	e.Open = e.Open || a.Open
	if e.State == dispatch.StateActive {
		f.openLocked(e)
		f.saveLocked()
		res := resultOf(e)
		f.mu.Unlock()
		return res, nil
	}
	if !f.connected() {
		f.setStateLocked(e, dispatch.StateWaiting)
		f.saveLocked()
		res := resultOf(e)
		f.mu.Unlock()
		return res, nil
	}
	f.setStateLocked(e, dispatch.StatePending)
	e.Error = ""
	ch, id, err := f.sendLocked(e)
	f.saveLocked()
	if err != nil || ch == nil {
		res := resultOf(e)
		f.mu.Unlock()
		return res, err
	}
	f.mu.Unlock()

	select {
	case <-ch:
	case <-time.After(replyWait):
		f.mu.Lock()
		delete(f.waiters, id)
		f.mu.Unlock()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if cur := f.m[a.Name]; cur != e {
		return dispatch.ExposeResult{}, fmt.Errorf("%s was removed", a.Name)
	}
	if e.State == dispatch.StateFailed {
		return dispatch.ExposeResult{}, errors.New(e.Error)
	}
	return resultOf(e), nil
}

func (f *forwards) unexpose(a dispatch.UnexposeArgs) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.m {
		if (a.Name != "" && e.Name == a.Name) || (a.Name == "" && a.Port != 0 && e.VMPort == a.Port) {
			f.cancelLocked(e)
			f.removeLocked(e, "unexpose")
			return nil
		}
	}
	return errors.New("not found")
}

func (f *forwards) removeLocked(e *forward, why string) {
	f.log.Info("forward removed", "name", e.Name, "vm_port", e.VMPort, "mac_port", e.MacPort, "state", e.State, "why", why)
	delete(f.waiters, e.LastExecID)
	delete(f.m, e.Name)
	f.saveLocked()
}

func (f *forwards) ports() []dispatch.PortInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]dispatch.PortInfo, 0, len(f.m))
	for _, e := range f.m {
		out = append(out, dispatch.PortInfo{Name: e.Name, VMPort: e.VMPort, MacPort: e.MacPort, State: e.State, Since: e.Since, Error: e.Error})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// onState replays forwards when the Mac connects and parks them when it leaves.
func (f *forwards) onState(s tether.State) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.m))
	for n := range f.m {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		e := f.m[n]
		if s != tether.Open {
			if e.State == dispatch.StateActive || e.State == dispatch.StatePending {
				f.setStateLocked(e, dispatch.StateWaiting)
			}
			continue
		}
		if e.State != dispatch.StateWaiting && e.State != dispatch.StatePending {
			continue
		}
		if _, _, err := f.sendLocked(e); err != nil {
			f.log.Warn("forward replay", "name", e.Name, "err", err)
		}
	}
	f.saveLocked()
}

func (f *forwards) onReady(_ context.Context, m dispatch.ForwardReady) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.m[m.Name]
	if e == nil || e.LastExecID != m.ID {
		f.log.Debug("forward_ready ignored", "name", m.Name, "id", m.ID)
		return
	}
	e.MacPort, e.Error = m.MacPort, ""
	f.setStateLocked(e, dispatch.StateActive)
	// open_url only for a CLI that is waiting; a replay must never reopen the browser.
	if ch, ok := f.waiters[m.ID]; ok {
		f.openLocked(e)
		ch <- waitResult{ok: true}
		delete(f.waiters, m.ID)
	}
	f.saveLocked()
}

func (f *forwards) onFailed(_ context.Context, m dispatch.ForwardFailed) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.m[m.Name]
	if e == nil || e.LastExecID != m.ID {
		f.log.Debug("forward_failed ignored", "name", m.Name, "id", m.ID)
		return
	}
	e.Error = m.Error
	f.setStateLocked(e, dispatch.StateFailed)
	if ch, ok := f.waiters[m.ID]; ok {
		ch <- waitResult{}
		delete(f.waiters, m.ID)
	}
	f.saveLocked()
}

// watch drops forwards whose port has stopped listening for watchMisses checks in a row.
func (f *forwards) watch(ctx context.Context) {
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		ports, err := listenPorts()
		if err != nil {
			continue
		}
		f.mu.Lock()
		for _, e := range f.m {
			if _, ok := ports[e.VMPort]; ok {
				e.misses = 0
				continue
			}
			if e.misses++; e.misses >= watchMisses {
				f.cancelLocked(e)
				f.removeLocked(e, "stopped listening")
			}
		}
		f.mu.Unlock()
	}
}

func friendly(err error) string { return strings.TrimPrefix(err.Error(), "dispatch: ") }

// hostIP is the container IP the Mac forwards to, looked up again if startup could not find it.
func (f *forwards) hostIP() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ip == "" {
		ip, err := containerIP()
		if err != nil {
			return "", err
		}
		f.ip = ip
	}
	return f.ip, nil
}
