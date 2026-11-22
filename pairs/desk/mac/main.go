// Command desk-mac shows the approval asks desk-vm forwards, one dialog at a time, and sends back the decision.
// It only ever displays what the VM sends; it never acts on it.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	deskv1 "github.com/hamshoes/devbox-tools/pairs/desk/gen/desk/v1"
	"github.com/hamshoes/devbox-tools/pairs/desk/internal/clean"
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
		fmt.Fprintln(os.Stderr, "desk-mac:", err)
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

	m := &mac{log: log, gw: gw, items: map[string]*item{}, wake: make(chan struct{}, 1)}
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/desk.v1.Desk/Ask", m.ask)
	tetherapp.Handle(app, "/desk.v1.Desk/Withdraw", m.withdraw)
	errc := make(chan error, 1)
	go func() { errc <- app.Serve(ctx, appSock) }()
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

type item struct {
	req        *deskv1.AskRequest
	state      string
	decision   string
	answeredAt time.Time
	acked      bool
	withdrawn  bool
	cancel     context.CancelFunc // ends the dialog while showing
}

type mac struct {
	log *slog.Logger
	gw  string

	mu    sync.Mutex
	items map[string]*item
	queue []string
	wake  chan struct{}
}

// ---- Desk/Ask, Desk/Withdraw ----

func (m *mac) ask(_ context.Context, r *deskv1.AskRequest) (*deskv1.AskResponse, error) {
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
		return &deskv1.AskResponse{State: it.state}, nil
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
	m.items[req.AskId] = &item{req: req, state: stateQueued}
	m.queue = append(m.queue, req.AskId)
	m.log.Info("ask queued", "ask_id", req.AskId, "kind", req.Kind, "run_name", req.Requester.RunName)
	select {
	case m.wake <- struct{}{}:
	default:
	}
	return &deskv1.AskResponse{State: stateQueued}, nil
}

func (m *mac) withdraw(_ context.Context, r *deskv1.WithdrawRequest) (*deskv1.WithdrawResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	it, ok := m.items[r.AskId]
	if !ok {
		return &deskv1.WithdrawResponse{}, nil
	}
	switch it.state {
	case stateQueued:
		for i, id := range m.queue {
			if id == r.AskId {
				m.queue = append(m.queue[:i], m.queue[i+1:]...)
				break
			}
		}
		delete(m.items, r.AskId)
		m.log.Info("ask withdrawn", "ask_id", r.AskId, "was", stateQueued)
	case stateShowing:
		it.withdrawn = true
		it.cancel() // the worker drops it and moves on; no answer is sent
		m.log.Info("ask withdrawn", "ask_id", r.AskId, "was", stateShowing)
	}
	return &deskv1.WithdrawResponse{}, nil
}

// sanitize re-checks everything the VM sent: it is shown to a person, so none of it is trusted.
func sanitize(r *deskv1.AskRequest) (*deskv1.AskRequest, error) {
	if !clean.IDPattern.MatchString(r.AskId) {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "bad ask_id")
	}
	if r.Kind != "git-push" && r.Kind != "generic" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "unknown kind")
	}
	title := clean.Line(r.Title, 120)
	if title == "" {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "empty title")
	}
	rq := r.Requester
	if rq == nil {
		rq = &deskv1.Requester{}
	}
	return &deskv1.AskRequest{
		AskId: r.AskId, Kind: r.Kind, Title: title,
		Detail: clean.Text(r.Detail, 2000), Cwd: clean.Line(r.Cwd, 256), CreatedAt: clean.Line(r.CreatedAt, 40),
		Requester: &deskv1.Requester{
			Uid: rq.Uid, User: clean.Line(rq.User, 64), Pid: rq.Pid,
			RunId: clean.Line(rq.RunId, 40), RunName: clean.Line(rq.RunName, 64),
		},
	}, nil
}

// ---- dialogs ----

// work shows queued asks one at a time, first in first out.
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

		title, body := dialogText(it.req)
		allow := decide(dctx, title, body)
		cancel()

		m.mu.Lock()
		if ctx.Err() != nil { // the session is ending; leave it unanswered
			m.mu.Unlock()
			return
		}
		if it.withdrawn {
			delete(m.items, id)
			m.mu.Unlock()
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

// dialogText is the dialog's title and body. Both go to osascript as arguments, never inside its script.
func dialogText(r *deskv1.AskRequest) (title, body string) {
	who := r.Requester.RunName
	if who == "" {
		who = r.Requester.User
	}
	title = "devbox · " + who
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
	req := &deskv1.AnswerRequest{AskId: id, Decision: it.decision, AnsweredAt: it.answeredAt.UTC().Format(time.RFC3339Nano)}
	m.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := tetherapp.Call(cctx, m.gw, "/desk.v1.DeskReplies/Answer", req, &deskv1.AnswerResponse{}); err != nil {
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
