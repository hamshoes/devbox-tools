// Command desk-vm forwards approval asks from the spool to its mac peer and writes the decisions back.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	deskv1 "github.com/hamshoes/devbox-tools/pairs/desk/gen/desk/v1"
	"github.com/hamshoes/devbox-tools/pairs/desk/internal/clean"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

var version = "dev"

const (
	maxAskBytes = 8 << 10
	tick        = time.Second
	resendEvery = time.Minute // the mac peer ignores duplicates; this covers an app that restarted under a live gateway
	answerTTL   = time.Hour
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "desk-vm:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	gw, appSock, err := tetherapp.FromEnv()
	if err != nil {
		return err
	}
	cfg, err := platform.Load()
	if err != nil {
		return err
	}
	base := cfg.NotifySpool
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := &daemon{
		log: log, gw: gw, owner: cfg.User, group: cfg.WorkGroup,
		asks: filepath.Join(base, "asks"), answers: filepath.Join(base, "answers"), cancel: filepath.Join(base, "cancel"),
		sent: map[string]time.Time{},
		http: &http.Client{Timeout: time.Second, Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dl net.Dialer
				return dl.DialContext(ctx, "unix", gw)
			},
		}},
	}

	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/desk.v1.DeskReplies/Answer", d.answer)
	errc := make(chan error, 1)
	go func() { errc <- app.Serve(ctx, appSock) }()
	go d.loop(ctx)
	log.Info("serving", "spool", base)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
}

type daemon struct {
	log                   *slog.Logger
	gw                    string
	owner, group          string
	asks, answers, cancel string
	http                  *http.Client

	mu        sync.Mutex // serialises answer writes
	sent      map[string]time.Time
	spoolErr  string // last spool problem logged, to log changes only
	lastClean time.Time
	warned    map[string]string
}

// ---- spool ----

// checkSpool reports why the spool can't be trusted, or nil. It never creates or changes anything.
func (d *daemon) checkSpool() error {
	u, err := user.Lookup(d.owner)
	if err != nil {
		return err
	}
	g, err := user.LookupGroup(d.group)
	if err != nil {
		return err
	}
	uid, _ := strconv.ParseUint(u.Uid, 10, 32)
	gid, _ := strconv.ParseUint(g.Gid, 10, 32)
	for _, c := range []struct {
		dir  string
		perm os.FileMode
	}{{d.asks, 0o770}, {d.answers, 0o750}, {d.cancel, 0o770}} {
		fi, err := os.Lstat(c.dir)
		if err != nil {
			return err
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.IsDir() {
			return fmt.Errorf("%s is not a directory", c.dir)
		}
		special := fi.Mode() & (os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
		if fi.Mode().Perm() != c.perm || special != os.ModeSetgid {
			return fmt.Errorf("%s has mode %v, want setgid %04o", c.dir, fi.Mode(), c.perm)
		}
		if uint64(st.Uid) != uid || uint64(st.Gid) != gid {
			return fmt.Errorf("%s is owned by %d:%d, want %s:%s", c.dir, st.Uid, st.Gid, d.owner, d.group)
		}
	}
	return nil
}

func (d *daemon) askPath(id string) string    { return filepath.Join(d.asks, id+".json") }
func (d *daemon) answerPath(id string) string { return filepath.Join(d.answers, id+".json") }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

type askFile struct {
	AskID     string `json:"ask_id"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Cwd       string `json:"cwd"`
	PID       int32  `json:"pid"`
	RunID     string `json:"run_id"`
	RunName   string `json:"run_name"`
	CreatedAt string `json:"created_at"`
}

// readAsk loads and validates asks/<id>.json. The requester's uid comes from the file's owner.
// A missing file is fs.ErrNotExist; anything else wrong is an error that makes the ask invalid.
func (d *daemon) readAsk(id string) (*deskv1.AskRequest, error) {
	f, err := os.OpenFile(d.askPath(id), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || !fi.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if fi.Size() > maxAskBytes {
		return nil, errors.New("too large")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxAskBytes+1))
	if err != nil || len(raw) > maxAskBytes {
		return nil, errors.New("unreadable or too large")
	}
	var a askFile
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.AskID != id {
		return nil, errors.New("ask_id does not match the file name")
	}
	if a.Kind != "git-push" && a.Kind != "generic" {
		return nil, errors.New("unknown kind")
	}
	title := clean.Line(a.Title, 120)
	if title == "" {
		return nil, errors.New("empty title")
	}
	created := clean.Line(a.CreatedAt, 40)
	if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
		created = fi.ModTime().UTC().Format(time.RFC3339Nano)
	}
	name := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	} else {
		name = "uid" + name
	}
	return &deskv1.AskRequest{
		AskId: id, Kind: a.Kind, Title: title,
		Detail: clean.Text(a.Detail, 2000), Cwd: clean.Line(a.Cwd, 256), CreatedAt: created,
		Requester: &deskv1.Requester{
			Uid: st.Uid, User: clean.Line(name, 64), Pid: max(a.PID, 0),
			RunId: clean.Line(a.RunID, 40), RunName: clean.Line(a.RunName, 64),
		},
	}, nil
}

type answerFile struct {
	Decision   string `json:"decision"`
	Note       string `json:"note"`
	AnsweredAt string `json:"answered_at"`
}

// writeAnswer records a decision for a pending ask. The first answer wins; it reports whether this call wrote it.
func (d *daemon) writeAnswer(id, decision, note string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !exists(d.askPath(id)) || exists(d.answerPath(id)) {
		return false, nil
	}
	raw, _ := json.Marshal(answerFile{Decision: decision, Note: note, AnsweredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	tmp, err := os.CreateTemp(d.answers, ".tmp-")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	_, werr := tmp.Write(append(raw, '\n'))
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr, os.Chmod(tmp.Name(), 0o640)); err != nil {
		return false, err
	}
	// Link, not rename: it fails if an answer is already there.
	if err := os.Link(tmp.Name(), d.answerPath(id)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// pendingIDs lists asks with no answer yet, oldest first.
func (d *daemon) pendingIDs() []string {
	ents, err := os.ReadDir(d.asks)
	if err != nil {
		return nil
	}
	type item struct {
		id string
		at time.Time
	}
	var items []item
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !clean.IDPattern.MatchString(id) || exists(d.answerPath(id)) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{id, fi.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].at.Before(items[j].at) })
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	return ids
}

// ---- loop ----

func (d *daemon) loop(ctx context.Context) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		d.tick(ctx)
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

func (d *daemon) tick(ctx context.Context) {
	if err := d.checkSpool(); err != nil {
		if msg := err.Error(); msg != d.spoolErr {
			d.spoolErr = msg
			d.log.Error("spool unusable; asks are not serviced", "err", err)
		}
		return
	}
	if d.spoolErr != "" {
		d.spoolErr = ""
		d.log.Info("spool ok")
	}

	connected := d.connected(ctx)
	if !connected {
		clear(d.sent)
	}

	reqs := map[string]*deskv1.AskRequest{}
	var order []string
	for _, id := range d.pendingIDs() {
		req, err := d.readAsk(id)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			if ok, _ := d.writeAnswer(id, "deny", "invalid ask"); ok {
				d.log.Warn("invalid ask denied", "ask_id", id, "err", err)
			}
		default:
			reqs[id] = req
			order = append(order, id)
		}
	}

	d.handleCancels(ctx, reqs, connected)
	for id, req := range reqs {
		if pid := req.Requester.Pid; pid > 0 && !exists("/proc/"+strconv.Itoa(int(pid))) {
			d.cancelAsk(ctx, req, "asker exited", connected)
			delete(reqs, id)
		}
	}
	if connected {
		d.send(ctx, order, reqs)
	}
	if time.Since(d.lastClean) > 30*time.Second {
		d.lastClean = time.Now()
		d.cleanup()
	}
}

func (d *daemon) connected(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://tether/healthz", nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var h struct {
		Connected bool `json:"connected"`
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h) == nil && h.Connected
}

// send forwards every pending ask not yet sent on this connection (or sent over resendEvery ago).
func (d *daemon) send(ctx context.Context, order []string, reqs map[string]*deskv1.AskRequest) {
	for _, id := range order {
		req, ok := reqs[id]
		if !ok || time.Since(d.sent[id]) < resendEvery {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		var resp deskv1.AskResponse
		err := tetherapp.Call(cctx, d.gw, "/desk.v1.Desk/Ask", req, &resp)
		cancel()
		var e *tetherapp.Error
		switch {
		case err == nil:
			if _, again := d.sent[id]; !again {
				d.log.Info("ask sent", "ask_id", id, "state", resp.State, "user", req.Requester.User, "run_name", req.Requester.RunName)
			}
			d.sent[id] = time.Now()
		case errors.As(err, &e) && (e.Code == tetherapp.CodeUnavailable || e.Code == tetherapp.CodeResourceExhausted):
			return // the peer is away or full; everything else would fail the same way. Retry next tick.
		default:
			if d.warned == nil {
				d.warned = map[string]string{}
			}
			if d.warned[id] != err.Error() {
				d.warned[id] = err.Error()
				d.log.Warn("ask not accepted", "ask_id", id, "err", err)
			}
		}
	}
}

// handleCancels answers cancelled for each cancel file's asks, then deletes the file. It drops cancelled asks from reqs.
func (d *daemon) handleCancels(ctx context.Context, reqs map[string]*deskv1.AskRequest, connected bool) {
	ents, err := os.ReadDir(d.cancel)
	if err != nil {
		return
	}
	for _, e := range ents {
		name := e.Name()
		var targets []string
		switch {
		case name == "all":
			for id := range reqs {
				targets = append(targets, id)
			}
		case clean.IDPattern.MatchString(name):
			if _, ok := reqs[name]; ok {
				targets = append(targets, name)
			} else if fi, err := e.Info(); err != nil || time.Since(fi.ModTime()) < time.Minute {
				continue // its ask may not be listed yet
			}
		default:
			continue
		}
		for _, id := range targets {
			d.cancelAsk(ctx, reqs[id], "cancelled in the VM", connected)
			delete(reqs, id)
		}
		os.Remove(filepath.Join(d.cancel, name))
	}
}

// cancelAsk answers cancelled and tells the mac peer to drop the ask, best effort.
func (d *daemon) cancelAsk(ctx context.Context, req *deskv1.AskRequest, reason string, connected bool) {
	id := req.AskId
	ok, err := d.writeAnswer(id, "cancelled", reason)
	if err != nil {
		d.log.Error("write answer failed", "ask_id", id, "err", err)
		return
	}
	if !ok {
		return
	}
	delete(d.sent, id)
	d.log.Info("ask cancelled", "ask_id", id, "reason", reason, "user", req.Requester.User, "run_name", req.Requester.RunName)
	if !connected {
		d.log.Info("withdraw skipped: peer not connected", "ask_id", id)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := tetherapp.Call(cctx, d.gw, "/desk.v1.Desk/Withdraw", &deskv1.WithdrawRequest{AskId: id, Reason: reason}, &deskv1.WithdrawResponse{}); err != nil {
		d.log.Warn("withdraw failed", "ask_id", id, "err", err)
		return
	}
	d.log.Info("withdraw sent", "ask_id", id)
}

// cleanup removes answers over answerTTL old, and the asks they answered. The ask goes first: an ask
// with no answer would look pending again.
func (d *daemon) cleanup() {
	ents, err := os.ReadDir(d.answers)
	if err != nil {
		return
	}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < answerTTL {
			continue
		}
		if id, ok := strings.CutSuffix(e.Name(), ".json"); ok && clean.IDPattern.MatchString(id) {
			os.Remove(d.askPath(id))
		}
		os.Remove(filepath.Join(d.answers, e.Name()))
	}
	for id := range d.warned {
		if !exists(d.askPath(id)) {
			delete(d.warned, id)
		}
	}
}

// ---- DeskReplies/Answer ----

func (d *daemon) answer(_ context.Context, r *deskv1.AnswerRequest) (*deskv1.AnswerResponse, error) {
	if !clean.IDPattern.MatchString(r.AskId) {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "bad ask_id")
	}
	if r.Decision != "allow" && r.Decision != "deny" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "decision must be allow or deny")
	}
	if err := d.checkSpool(); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeUnavailable, "spool unusable")
	}
	req, err := d.readAsk(r.AskId)
	if err != nil {
		d.log.Debug("answer for an ask that is gone or invalid; ignored", "ask_id", r.AskId)
		return &deskv1.AnswerResponse{}, nil
	}
	ok, err := d.writeAnswer(r.AskId, r.Decision, clean.Line(r.Note, 200))
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "write answer: %v", err)
	}
	if ok {
		d.log.Info("ask answered", "ask_id", r.AskId, "decision", r.Decision, "user", req.Requester.User, "uid", req.Requester.Uid, "run_name", req.Requester.RunName)
	}
	return &deskv1.AnswerResponse{}, nil
}
