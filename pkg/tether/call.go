package tether

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
)

// RemoteError is an error returned by the peer's handler. Code is
// application-defined; handlers that return another error type send "internal".
type RemoteError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("tether: remote error %s: %s", e.Code, e.Message)
}

type replyKey struct{}

// replyFunc queues one encoded reply frame on the connection a request arrived on.
type replyFunc func(frame []byte)

// Call sends req and waits for the peer's reply. There are no retries: it fails
// with ErrNotConnected at once if the connection is down or drops while waiting,
// with ctx's error if ctx ends first, and with *RemoteError if the handler failed.
func Call[Req, Resp Message](ctx context.Context, p Peer, req Req) (Resp, error) {
	var zero Resp
	e, err := p.roundTrip(ctx, req)
	if err != nil {
		return zero, err
	}
	if e.Err != nil {
		return zero, e.Err
	}
	return Decode[Resp](e)
}

// Handle registers fn for requests of type Req, replacing any earlier handler.
// Unlike On, each request runs in its own goroutine, so a slow fn never blocks
// the connection. ctx ends when the connection does.
func Handle[Req, Resp Message](r *Router, fn func(ctx context.Context, req Req) (Resp, error)) {
	var zero Req
	typ := zero.MessageType()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[typ] = func(ctx context.Context, e Envelope) {
		reply, _ := ctx.Value(replyKey{}).(replyFunc)
		fail := func(code, msg string) {
			if e.ID == 0 || reply == nil {
				r.log.Warn("tether: dropping request", "type", typ, "err", msg)
				return
			}
			var none Resp
			if frame, err := encode(none, 0, e.ID, &RemoteError{Code: code, Message: msg}); err == nil {
				reply(frame)
			}
		}
		req, err := Decode[Req](e)
		if err != nil {
			fail("invalid_argument", err.Error())
			return
		}
		go func() {
			defer func() {
				if p := recover(); p != nil {
					r.log.Error("tether: handler panicked", "type", typ, "panic", p, "stack", string(debug.Stack()))
					fail("internal", "handler panicked")
				}
			}()
			resp, err := fn(ctx, req)
			if err != nil {
				var re *RemoteError
				if errors.As(err, &re) {
					fail(re.Code, re.Message)
				} else {
					fail("internal", err.Error())
				}
				return
			}
			if e.ID == 0 || reply == nil {
				return
			}
			frame, err := encode(resp, 0, e.ID, nil)
			if err != nil {
				fail("internal", err.Error())
				return
			}
			reply(frame)
		}()
	}
}

func (l *link) register(id uint64) chan Envelope {
	ch := make(chan Envelope, 1)
	l.pmu.Lock()
	l.pending[id] = ch
	l.pmu.Unlock()
	return ch
}

func (l *link) forget(id uint64) {
	l.pmu.Lock()
	delete(l.pending, id)
	l.pmu.Unlock()
}

func (b *base) roundTrip(ctx context.Context, m Message) (Envelope, error) {
	b.mu.Lock()
	l := b.link
	b.mu.Unlock()
	if l == nil {
		return Envelope{}, ErrNotConnected
	}
	id := b.nextID.Add(1)
	ch := l.register(id)
	defer l.forget(id)
	frame, err := encode(m, id, 0, nil)
	if err != nil {
		return Envelope{}, err
	}
	select {
	case l.out <- frame:
	case <-l.done:
		return Envelope{}, ErrNotConnected
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
	select {
	case e := <-ch:
		return e, nil
	case <-l.done:
		return Envelope{}, ErrNotConnected
	case <-ctx.Done():
		return Envelope{}, ctx.Err()
	}
}

// receive routes one frame: replies go to the waiting Call, everything else to the router.
func (b *base) receive(ctx context.Context, l *link, frame []byte) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		b.log.Warn("tether: dropping malformed envelope", "err", err)
		return
	}
	if e.Re != 0 {
		l.pmu.Lock()
		ch := l.pending[e.Re]
		l.pmu.Unlock()
		if ch == nil {
			b.log.Debug("tether: dropping reply nobody is waiting for", "re", e.Re)
			return
		}
		select {
		case ch <- e:
		default:
		}
		return
	}
	reply := replyFunc(func(frame []byte) {
		select {
		case l.out <- frame:
		case <-l.done:
		}
	})
	b.router.dispatch(context.WithValue(ctx, replyKey{}, reply), e)
}
