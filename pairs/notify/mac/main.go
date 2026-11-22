// Command notify-mac is the Mac half of the notify pair. It shows the VM's
// notifications, shows its approval asks one dialog at a time and sends back
// the decisions, and serves the same dialogs to the other Mac pair apps on the
// approval socket. It only ever displays what the VM sends; it never acts on it.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/hamshoes/devbox-tools/pairs/notify/approve"
	notifyv1 "github.com/hamshoes/devbox-tools/pairs/notify/gen/notify/v1"
	"github.com/hamshoes/devbox-tools/pairs/notify/internal/clean"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

var version = "dev"

const (
	maxActive     = 20
	retryEvery    = 5 * time.Second
	answeredKeep  = 2 * time.Hour
	stateQueued   = "queued"
	stateShowing  = "showing"
	stateAnswered = "answered"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "notify-mac:", err)
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
	if err := checkFlags(); err != nil {
		return err
	}
	gw, appSock, err := tetherapp.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	m := &mac{log: log, gw: gw, items: map[string]*item{}, wake: make(chan struct{}, 1), notes: newRateLimit(notifyBurst, notifyEvery)}
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/notify.v1.Notify/Notify", m.notify)
	tetherapp.Handle(app, "/notify.v1.Notify/Ask", m.ask)
	tetherapp.Handle(app, "/notify.v1.Notify/Withdraw", m.withdraw)
	errc := make(chan error, 2)
	go func() { errc <- app.Serve(ctx, appSock) }()
	if sock := os.Getenv(approve.EnvSocket); sock != "" {
		go func() { errc <- m.serveApprovals(ctx, sock) }()
	} else {
		log.Warn("no approval socket: the other Mac pair apps will be denied", "env", approve.EnvSocket)
	}
	go m.work(ctx)
	go m.retryAnswers(ctx)

	// On SIGTERM nothing is answered: unanswered asks stay pending on the VM and come back next session.
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		return nil
	}
}

// item is one dialog: a VM ask (req set) or a local approval (local set).
type item struct {
	id          string
	title, body string
	req         *notifyv1.AskRequest
	local       chan bool // buffered; receives the decision of a local approval

	state      string
	decision   string
	answeredAt time.Time
	acked      bool
	withdrawn  bool
	cancel     context.CancelFunc // ends the dialog while showing
}

type mac struct {
	log   *slog.Logger
	gw    string
	notes *rateLimit

	mu     sync.Mutex
	items  map[string]*item
	queue  []string
	wake   chan struct{}
	nextID int
}

// enqueueLocked adds a dialog to the end of the queue and wakes the worker.
func (m *mac) enqueueLocked(it *item) {
	it.state = stateQueued
	m.items[it.id] = it
	m.queue = append(m.queue, it.id)
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// withdrawLocked drops a dialog: unqueued if waiting, closed if showing.
// It reports whether the item was still undecided.
func (m *mac) withdrawLocked(id string) bool {
	it, ok := m.items[id]
	if !ok {
		return false
	}
	switch it.state {
	case stateQueued:
		for i, q := range m.queue {
			if q == id {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		delete(m.items, id)
		return true
	case stateShowing:
		it.withdrawn = true
		it.cancel() // the worker drops it and moves on; no answer is sent
		return true
	}
	return false
}

// ---- Notify/Ask, Notify/Withdraw ----

func (m *mac) ask(_ context.Context, r *notifyv1.AskRequest) (*notifyv1.AskResponse, error) {
	req, err := sanitize(r)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if it, ok := m.items[req.AskId]; ok {
		if it.state == stateAnswered {
			go m.sendAnswer(context.Background(), req.AskId) // the VM may have missed it
		}
		return &notifyv1.AskResponse{State: it.state}, nil
	}
	active := 0
	for _, it := range m.items {
		if it.state != stateAnswered {
			active++
		}
	}
	if active >= maxActive {
		return nil, tetherapp.Errorf(tetherapp.CodeResourceExhausted, "%d asks already waiting", maxActive)
	}
	title, body := dialogText(req)
	m.enqueueLocked(&item{id: req.AskId, title: title, body: body, req: req})
	m.log.Info("ask queued", "ask_id", req.AskId, "kind", req.Kind, "run_name", req.Requester.RunName)
	return &notifyv1.AskResponse{State: stateQueued}, nil
}

func (m *mac) withdraw(_ context.Context, r *notifyv1.WithdrawRequest) (*notifyv1.WithdrawResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if it, ok := m.items[r.AskId]; ok && it.req != nil && m.withdrawLocked(r.AskId) {
		m.log.Info("ask withdrawn", "ask_id", r.AskId)
	}
	return &notifyv1.WithdrawResponse{}, nil
}

// sanitize re-checks everything the VM sent: it is shown to a person, so none of it is trusted.
func sanitize(r *notifyv1.AskRequest) (*notifyv1.AskRequest, error) {
	if !clean.IDPattern.MatchString(r.AskId) {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "bad ask_id")
	}
	if r.Kind != "git-push" && r.Kind != "git-push-env" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "unknown kind")
	}
	title := clean.Line(r.Title, 120)
	if title == "" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "empty title")
	}
	rq := r.Requester
	if rq == nil {
		rq = &notifyv1.Requester{}
	}
	return &notifyv1.AskRequest{
		AskId: r.AskId, Kind: r.Kind, Title: title,
		Detail: clean.Text(r.Detail, 2000), Cwd: clean.Line(r.Cwd, 256), CreatedAt: clean.Line(r.CreatedAt, 40),
		Requester: &notifyv1.Requester{
			Uid: rq.Uid, User: clean.Line(rq.User, 64), Pid: rq.Pid,
			RunId: clean.Line(rq.RunId, 40), RunName: clean.Line(rq.RunName, 64),
		},
	}, nil
}

// ---- dialogs ----

// work shows queued dialogs one at a time, first in first out.
func (m *mac) work(ctx context.Context) {
	for {
		m.mu.Lock()
		if len(m.queue) == 0 {
			m.mu.Unlock()
			select {
			case <-m.wake:
				continue
			case <-ctx.Done():
				return
			}
		}
		id := m.queue[0]
		m.queue = m.queue[1:]
		it := m.items[id]
		dctx, cancel := context.WithCancel(ctx)
		it.state, it.cancel = stateShowing, cancel
		m.mu.Unlock()

		allow := decide(dctx, it.title, it.body)
		cancel()

		m.mu.Lock()
		if ctx.Err() != nil { // the session is ending; leave VM asks unanswered
			m.mu.Unlock()
			if it.local != nil {
				it.local <- false
			}
			return
		}
		if it.withdrawn {
			delete(m.items, id)
			m.mu.Unlock()
			if it.local != nil {
				it.local <- false
			}
			continue
		}
		if it.local != nil {
			delete(m.items, id)
			m.mu.Unlock()
			it.local <- allow
			m.log.Info("approval decided", "id", id, "allow", allow)
			continue
		}
		it.state, it.decision, it.answeredAt = stateAnswered, "deny", time.Now()
		if allow {
			it.decision = "allow"
		}
		m.mu.Unlock()
		m.log.Info("ask decided", "ask_id", id, "decision", it.decision, "run_name", it.req.Requester.RunName)
		m.sendAnswer(ctx, id)
	}
}

// dialogText is a VM ask's dialog title and body. Both go to osascript as arguments, never inside its script.
func dialogText(r *notifyv1.AskRequest) (title, body string) {
	who := r.Requester.RunName
	if who == "" {
		who = r.Requester.User
	}
	title = "box · " + who
	body = fmt.Sprintf("%s\n\n%s\n\n%s\nuid %d (%s) · pid %s",
		r.Title, r.Detail, r.Cwd, r.Requester.Uid, r.Requester.User, strconv.Itoa(int(r.Requester.Pid)))
	return title, body
}

// ---- answers ----

// sendAnswer sends a decided ask's answer, once; retryAnswers repeats it until the VM has taken it.
func (m *mac) sendAnswer(ctx context.Context, id string) {
	m.mu.Lock()
	it, ok := m.items[id]
	if !ok || it.state != stateAnswered || it.acked {
		m.mu.Unlock()
		return
	}
	req := &notifyv1.AnswerRequest{AskId: id, Decision: it.decision, AnsweredAt: it.answeredAt.UTC().Format(time.RFC3339Nano)}
	m.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tetherapp.Call(cctx, m.gw, "/notify.v1.NotifyReplies/Answer", req, &notifyv1.AnswerResponse{}); err != nil {
		m.log.Debug("answer not delivered; will retry", "ask_id", id, "err", err)
		return
	}
	m.mu.Lock()
	it.acked = true
	m.mu.Unlock()
}

func (m *mac) retryAnswers(ctx context.Context) {
	t := time.NewTicker(retryEvery)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
		var due []string
		m.mu.Lock()
		for id, it := range m.items {
			switch {
			case it.state != stateAnswered:
			case !it.acked:
				due = append(due, id)
			case time.Since(it.answeredAt) > answeredKeep:
				delete(m.items, id)
			}
		}
		m.mu.Unlock()
		for _, id := range due {
			m.sendAnswer(ctx, id)
		}
	}
}

// ---- Notify/Notify ----

const (
	notifyBurst = 10
	notifyEvery = 6 * time.Second // one more notification allowed per interval, up to the burst
)

func (m *mac) notify(ctx context.Context, r *notifyv1.NotifyRequest) (*notifyv1.NotifyResponse, error) {
	title, text := clean.Line(r.Title, 80), clean.Text(r.Text, 400)
	if title == "" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "empty title")
	}
	if !m.notes.allow() {
		return nil, tetherapp.Errorf(tetherapp.CodeResourceExhausted, "too many notifications")
	}
	if err := showNotification(ctx, "box · "+title, text); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "show notification: %v", err)
	}
	return &notifyv1.NotifyResponse{}, nil
}

// rateLimit is a token bucket: burst tokens, one more every interval.
type rateLimit struct {
	mu     sync.Mutex
	tokens float64
	burst  float64
	every  time.Duration
	last   time.Time
}

func newRateLimit(burst int, every time.Duration) *rateLimit {
	return &rateLimit{tokens: float64(burst), burst: float64(burst), every: every, last: time.Now()}
}

func (r *rateLimit) allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.tokens = min(r.burst, r.tokens+float64(now.Sub(r.last))/float64(r.every))
	r.last = now
	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}

var errQueueFull = errors.New("too many dialogs waiting")
