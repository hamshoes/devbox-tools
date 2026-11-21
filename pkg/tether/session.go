package tether

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	defaultPingInterval = 20 * time.Second
	defaultPingTimeout  = 10 * time.Second
	defaultMaxMessage   = 1 << 20
	closeBudget         = 5 * time.Second
)

// ErrNotConnected is returned by Send when no connection is open.
var ErrNotConnected = errors.New("tether: not connected")

// Peer is what both sides expose once running.
type Peer interface {
	// Send queues m on the open connection. Nothing is queued while the
	// connection is down; it returns ErrNotConnected instead.
	Send(ctx context.Context, m Message) error
	// roundTrip is the transport under Call.
	roundTrip(ctx context.Context, m Message) (Envelope, error)
	State() State
	// OnState registers fn for every state change, called from a single
	// goroutine. The current state is delivered to fn first.
	OnState(fn func(State))
	// Close is idempotent and returns within 5 seconds.
	Close() error
}

// link is one live connection's outbound queue.
type link struct {
	out  chan []byte
	done chan struct{}

	pmu     sync.Mutex
	pending map[uint64]chan Envelope
}

// base is the half of a Peer shared by server and client.
type base struct {
	router      *Router
	log         *slog.Logger
	bus         *bus
	ping        time.Duration
	pingTimeout time.Duration
	maxMessage  int64

	nextID atomic.Uint64

	mu   sync.Mutex
	link *link
}

func newBase(r *Router, log *slog.Logger, ping, pingTimeout time.Duration, maxMessage int64) *base {
	if log == nil {
		log = slog.Default()
	}
	if ping <= 0 {
		ping = defaultPingInterval
	}
	if pingTimeout <= 0 {
		pingTimeout = defaultPingTimeout
	}
	if maxMessage <= 0 {
		maxMessage = defaultMaxMessage
	}
	return &base{router: r, log: log, bus: newBus(), ping: ping, pingTimeout: pingTimeout, maxMessage: maxMessage}
}

func (b *base) Send(ctx context.Context, m Message) error {
	b.mu.Lock()
	l := b.link
	b.mu.Unlock()
	if l == nil {
		return ErrNotConnected
	}
	data, err := Encode(m)
	if err != nil {
		return err
	}
	select {
	case l.out <- data:
		return nil
	case <-l.done:
		return ErrNotConnected
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *base) State() State           { return b.bus.get() }
func (b *base) OnState(fn func(State)) { b.bus.subscribe(fn) }

// run serves one connection until it ends, then returns. It has a single
// writer, so messages leave in the order they were sent. When parent is
// cancelled the connection is closed gracefully.
func (b *base) run(parent context.Context, conn *websocket.Conn) {
	conn.SetReadLimit(b.maxMessage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := &link{out: make(chan []byte, 64), done: make(chan struct{}), pending: map[uint64]chan Envelope{}}
	b.mu.Lock()
	b.link = l
	b.mu.Unlock()
	b.bus.set(Open)

	var wg sync.WaitGroup
	end := func(why string, err error) {
		b.log.Debug("tether: connection ending", "why", why, "err", err)
		cancel()
	}

	wg.Go(func() { // reader
		for {
			typ, frame, err := conn.Read(ctx)
			if err != nil {
				end("read", err)
				return
			}
			if typ != websocket.MessageText {
				b.log.Warn("tether: dropping non-text frame")
				continue
			}
			b.receive(ctx, l, frame)
		}
	})
	wg.Go(func() { // writer
		for {
			select {
			case data := <-l.out:
				if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
					end("write", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})
	wg.Go(func() { // heartbeat
		t := time.NewTicker(b.ping)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, b.pingTimeout)
				err := conn.Ping(pctx)
				pcancel()
				if err != nil {
					end("ping", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	})
	wg.Go(func() { // graceful shutdown
		select {
		case <-parent.Done():
			closed := make(chan struct{})
			go func() {
				conn.Close(websocket.StatusNormalClosure, "")
				close(closed)
			}()
			select {
			case <-closed:
			case <-time.After(closeBudget / 2):
			}
			cancel()
		case <-ctx.Done():
		}
	})

	<-ctx.Done()
	b.mu.Lock()
	b.link = nil
	b.mu.Unlock()
	close(l.done)
	conn.CloseNow()
	wg.Wait()
}
