package main

import (
	"context"
	"log/slog"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const (
	portsPoll        = 2 * time.Second
	portsDebounce    = time.Second
	portsSendTimeout = 5 * time.Second
	maxCmdChars      = 256
)

// portWatcher reports the container's listening TCP ports to the Mac as
// ControlEvents/PortsChanged.
type portWatcher struct {
	log    *slog.Logger
	cfg    *platform.Config
	gwSock string
	owners ownerCache
}

// run polls until ctx ends. A snapshot goes out on start and whenever the
// control gateway comes back; in between, changes are sent debounced.
func (w *portWatcher) run(ctx context.Context) {
	needSnap := true
	var sent map[int]*controlv1.Listener
	var flush <-chan time.Time

	send := func(req *controlv1.PortsChangedRequest, cur map[int]*controlv1.Listener) {
		sctx, cancel := context.WithTimeout(ctx, portsSendTimeout)
		defer cancel()
		err := tetherapp.Call(sctx, w.gwSock, "/control.v1.ControlEvents/PortsChanged", req, &controlv1.PortsChangedResponse{})
		if err != nil {
			// Dropped: the next connection sends a snapshot.
			if ctx.Err() == nil {
				w.log.Info("ports changed not sent", "err", err)
			}
			needSnap, sent = true, nil
			return
		}
		needSnap, sent = false, cur
		w.log.Info("ports changed", "snapshot", req.Snapshot, "opened", len(req.Opened), "closed", req.Closed)
	}

	step := func() {
		if !gwConnected(ctx, w.gwSock) {
			needSnap, sent, flush = true, nil, nil
			return
		}
		if flush != nil && !needSnap {
			return
		}
		cur, err := w.scan()
		if err != nil {
			return
		}
		switch {
		case needSnap:
			flush = nil
			send(&controlv1.PortsChangedRequest{Snapshot: true, Opened: sortedListeners(cur), Host: hostOrEmpty()}, cur)
		default:
			if opened, closed := diffListeners(sent, cur); len(opened)+len(closed) > 0 {
				flush = time.After(portsDebounce)
			}
		}
	}

	step()
	t := time.NewTicker(portsPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		case <-flush:
			flush = nil
			// Rescan: a port that opened and closed inside the window is not reported.
			cur, err := w.scan()
			if err != nil || needSnap {
				continue
			}
			if opened, closed := diffListeners(sent, cur); len(opened)+len(closed) > 0 {
				send(&controlv1.PortsChangedRequest{Opened: opened, Closed: closed, Host: hostOrEmpty()}, cur)
			}
		}
	}
}

// diffListeners returns what opened (or changed address) and which ports closed since sent.
func diffListeners(sent, cur map[int]*controlv1.Listener) (opened []*controlv1.Listener, closed []uint32) {
	for p, l := range cur {
		if o, ok := sent[p]; !ok || o.Address != l.Address {
			opened = append(opened, l)
		}
	}
	for p := range sent {
		if _, ok := cur[p]; !ok {
			closed = append(closed, uint32(p))
		}
	}
	slices.SortFunc(opened, func(a, b *controlv1.Listener) int { return int(a.Port) - int(b.Port) })
	slices.Sort(closed)
	return
}

func sortedListeners(m map[int]*controlv1.Listener) []*controlv1.Listener {
	out := make([]*controlv1.Listener, 0, len(m))
	for _, l := range m {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b *controlv1.Listener) int { return int(a.Port) - int(b.Port) })
	return out
}

// scan lists the listening ports worth reporting, by port. A port bound on
// several addresses is described by its widest binding: IPv4 beats IPv6 beats loopback.
func (w *portWatcher) scan() (map[int]*controlv1.Listener, error) {
	rows, err := procListeners()
	if err != nil {
		return nil, err
	}
	best := map[int]listenRow{}
	loopbackOnly := map[int]bool{}
	for _, r := range rows {
		if w.cfg.IsReserved(r.port) {
			continue
		}
		if _, seen := loopbackOnly[r.port]; !seen {
			loopbackOnly[r.port] = true
		}
		loopbackOnly[r.port] = loopbackOnly[r.port] && r.ip.IsLoopback()
		if b, ok := best[r.port]; !ok || rank(r.ip) > rank(b.ip) {
			best[r.port] = r
		}
	}
	inodes := make(map[uint64]bool, len(best))
	for _, r := range best {
		inodes[r.inode] = true
	}
	w.owners.resolve(inodes)

	out := make(map[int]*controlv1.Listener, len(best))
	for p, r := range best {
		l := &controlv1.Listener{Port: uint32(p), Address: r.ip.String(), LoopbackOnly: loopbackOnly[p]}
		if pid := w.owners.pid[r.inode]; pid > 0 {
			l.Pid = pid
			l.Process, l.Cmd = procInfo(pid)
		}
		out[p] = l
	}
	return out, nil
}

// hostOrEmpty is the container IP for PortsChanged, or "" if it can't be found.
func hostOrEmpty() string {
	ip, _ := containerIP()
	return ip
}

func rank(ip net.IP) int {
	switch {
	case ip.IsLoopback():
		return 0
	case ip.To4() == nil:
		return 1
	}
	return 2
}

// ownerCache maps socket inodes to the pid that holds them, so /proc is only
// scanned when a new listening socket appears. Unreadable owners (other uids) stay 0.
type ownerCache struct{ pid map[uint64]int32 }

func (o *ownerCache) resolve(want map[uint64]bool) {
	if o.pid == nil {
		o.pid = map[uint64]int32{}
	}
	for ino, pid := range o.pid {
		if !want[ino] {
			delete(o.pid, ino)
		} else if pid > 0 {
			if _, err := os.Stat("/proc/" + strconv.Itoa(int(pid))); err != nil {
				delete(o.pid, ino)
			}
		}
	}
	missing := map[uint64]bool{}
	for ino := range want {
		if _, ok := o.pid[ino]; !ok {
			missing[ino] = true
		}
	}
	if len(missing) == 0 {
		return
	}
	pids, _ := os.ReadDir("/proc")
	for _, e := range pids {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := "/proc/" + e.Name() + "/fd/"
		fds, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			link, err := os.Readlink(dir + fd.Name())
			if err != nil || !strings.HasPrefix(link, "socket:[") {
				continue
			}
			ino, err := strconv.ParseUint(strings.TrimSuffix(link[len("socket:["):], "]"), 10, 64)
			if err == nil && missing[ino] {
				o.pid[ino] = int32(pid)
				delete(missing, ino)
			}
		}
		if len(missing) == 0 {
			return
		}
	}
	for ino := range missing {
		o.pid[ino] = 0
	}
}

// procInfo is a process's comm and its argv joined with spaces, truncated.
func procInfo(pid int32) (comm, cmd string) {
	p := "/proc/" + strconv.Itoa(int(pid))
	if b, err := os.ReadFile(p + "/comm"); err == nil {
		comm = strings.ToValidUTF8(strings.TrimSpace(string(b)), "?")
	}
	return comm, procCmd(pid)
}

// procCmd is the argv of pid joined with spaces, truncated to maxCmdChars characters.
func procCmd(pid int32) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/cmdline")
	if err != nil {
		return ""
	}
	s := strings.ToValidUTF8(strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " ")), "?")
	if r := []rune(s); len(r) > maxCmdChars {
		s = string(r[:maxCmdChars])
	}
	return s
}
