// Package tether sends typed, fire-and-forget JSON messages over a WebSocket
// between one server and one client.
package tether

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Message is any payload type; it names its own wire type.
type Message interface{ MessageType() string }

// Envelope is the wire format: one per WebSocket text frame.
type Envelope struct {
	Type string          `json:"type"`
	Sent time.Time       `json:"sent"`
	Data json.RawMessage `json:"data"`
}

// Encode wraps m in an envelope and returns its JSON.
func Encode(m Message) ([]byte, error) {
	data, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("tether: encode %s: %w", m.MessageType(), err)
	}
	return json.Marshal(Envelope{Type: m.MessageType(), Sent: time.Now().UTC(), Data: data})
}

// Decode unpacks e into a T. T should be a value type, not a pointer.
func Decode[T Message](e Envelope) (T, error) {
	var v T
	if want := v.MessageType(); e.Type != want {
		return v, fmt.Errorf("tether: envelope type %q is not %q", e.Type, want)
	}
	if err := json.Unmarshal(e.Data, &v); err != nil {
		return v, fmt.Errorf("tether: decode %s: %w", e.Type, err)
	}
	return v, nil
}

// Router dispatches incoming envelopes by Type.
type Router struct {
	log      *slog.Logger
	mu       sync.RWMutex
	handlers map[string]func(context.Context, Envelope)
}

func NewRouter(log *slog.Logger) *Router {
	if log == nil {
		log = slog.Default()
	}
	return &Router{log: log, handlers: map[string]func(context.Context, Envelope){}}
}

// On registers fn under T's MessageType(), replacing any earlier handler.
// Handlers run on the connection's reader goroutine, in arrival order.
func On[T Message](r *Router, fn func(ctx context.Context, m T)) {
	var zero T
	typ := zero.MessageType()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[typ] = func(ctx context.Context, e Envelope) {
		m, err := Decode[T](e)
		if err != nil {
			r.log.Warn("tether: dropping message", "type", typ, "err", err)
			return
		}
		fn(ctx, m)
	}
}

// dispatch never fails: anything it cannot deliver is logged and dropped.
func (r *Router) dispatch(ctx context.Context, frame []byte) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		r.log.Warn("tether: dropping malformed envelope", "err", err)
		return
	}
	r.mu.RLock()
	h := r.handlers[e.Type]
	r.mu.RUnlock()
	if h == nil {
		r.log.Warn("tether: dropping message of unknown type", "type", e.Type)
		return
	}
	defer func() {
		if p := recover(); p != nil {
			r.log.Error("tether: handler panicked", "type", e.Type, "panic", p)
		}
	}()
	h(ctx, e)
}
