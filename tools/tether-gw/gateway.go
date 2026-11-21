package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/tether"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/dynamicpb"
)

const (
	callTimeout  = 30 * time.Second
	helloTimeout = 10 * time.Second
	codeMismatch = "failed_precondition"
)

type gateway struct {
	side      string
	other     string
	sc        *schema
	log       *slog.Logger
	appSocket string

	peer tether.Peer

	mu          sync.Mutex
	gen         int // bumped on every link state change
	helloOK     bool
	peerVersion string
	reason      string
}

func newGateway(side string, sc *schema, appSocket string, log *slog.Logger) *gateway {
	other := "mac"
	if side == "mac" {
		other = "vm"
	}
	return &gateway{side: side, other: other, sc: sc, appSocket: appSocket, log: log}
}

func (g *gateway) connected() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.helloOK && g.peer != nil && g.peer.State() == tether.Open
}

func (g *gateway) setReason(r string) {
	g.mu.Lock()
	g.reason = r
	g.mu.Unlock()
}

// onState resets the handshake on every link change; the mac side then says hello.
func (g *gateway) onState(ctx context.Context, st tether.State) {
	g.mu.Lock()
	g.gen++
	gen := g.gen
	g.helloOK = false
	g.peerVersion = ""
	g.mu.Unlock()
	g.log.Info("link state", "state", st.String())
	if st == tether.Open && g.side == "mac" {
		go g.sayHello(ctx, gen)
	}
}

func (g *gateway) stale(gen int) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.gen != gen
}

func (g *gateway) hello() Hello {
	return Hello{Pair: g.sc.man.Pair, Version: g.sc.man.Version, SchemaSHA256: g.sc.sha}
}

// check compares what the peer claims with this side's own pair.
func (g *gateway) check(pair, sha string) string {
	switch {
	case pair != g.sc.man.Pair:
		return "pair name mismatch: peer runs " + strconv.Quote(pair) + ", this side runs " + strconv.Quote(g.sc.man.Pair)
	case sha != g.sc.sha:
		return "schema hash mismatch: peer " + sha + ", this side " + g.sc.sha
	}
	return ""
}

// sayHello is the mac side's handshake. A refusal is final: it closes the link
// rather than reconnecting into the same mismatch.
func (g *gateway) sayHello(ctx context.Context, gen int) {
	for !g.stale(gen) {
		hctx, cancel := context.WithTimeout(ctx, helloTimeout)
		r, err := tether.Call[Hello, HelloReply](hctx, g.peer, g.hello())
		cancel()
		var re *tether.RemoteError
		switch {
		case err == nil:
			if why := g.check(r.Pair, r.SchemaSHA256); why != "" {
				g.refuse(why)
				return
			}
			g.mu.Lock()
			if g.gen == gen {
				g.helloOK = true
				g.peerVersion = r.Version
				g.reason = ""
			}
			g.mu.Unlock()
			if r.Version != g.sc.man.Version {
				g.log.Warn("peer version differs", "version", g.sc.man.Version, "peer_version", r.Version)
			}
			g.log.Info("connected", "peer_version", r.Version)
			return
		case errors.Is(err, tether.ErrNotConnected):
			return
		case errors.As(err, &re) && re.Code == codeMismatch:
			g.refuse("peer refused: " + re.Message)
			return
		}
		g.log.Warn("hello failed; retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (g *gateway) refuse(why string) {
	g.setReason(why)
	g.log.Error("refusing link", "why", why)
	go g.peer.Close()
}

// handleHello is the vm side of the handshake.
func (g *gateway) handleHello(_ context.Context, h Hello) (HelloReply, error) {
	reply := HelloReply{Pair: g.sc.man.Pair, Version: g.sc.man.Version, SchemaSHA256: g.sc.sha}
	if why := g.check(h.Pair, h.SchemaSHA256); why != "" {
		g.setReason(why)
		g.log.Error("refusing link", "why", why)
		return reply, &tether.RemoteError{Code: codeMismatch, Message: why}
	}
	g.mu.Lock()
	g.helloOK = true
	g.peerVersion = h.Version
	g.reason = ""
	g.mu.Unlock()
	if h.Version != g.sc.man.Version {
		g.log.Warn("peer version differs", "version", g.sc.man.Version, "peer_version", h.Version)
	}
	g.log.Info("connected", "peer_version", h.Version)
	return reply, nil
}

func remoteErr(e *tetherapp.Error) *tether.RemoteError {
	return &tether.RemoteError{Code: e.Code, Message: e.Message}
}

// validate decodes payload as d and returns its canonical protojson.
func (g *gateway) validate(d method, payload []byte, in bool) ([]byte, *tetherapp.Error) {
	desc := d.out
	if in {
		desc = d.in
	}
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	msg := dynamicpb.NewMessage(desc)
	if err := (protojson.UnmarshalOptions{Resolver: g.sc.types}).Unmarshal(payload, msg); err != nil {
		code := tetherapp.CodeInvalidArgument
		if !in {
			code = tetherapp.CodeInternal
		}
		return nil, tetherapp.Errorf(code, "%s: %v", desc.FullName(), err)
	}
	return canonical(msg, g.sc), nil
}

func canonical(m proto.Message, sc *schema) []byte {
	out, _ := protojson.MarshalOptions{Resolver: sc.types}.Marshal(m)
	return out
}

// handleCall is the inbound path: the peer gateway asks this side's app.
func (g *gateway) handleCall(ctx context.Context, c GwCall) (GwReply, error) {
	start := time.Now()
	out, err := g.inbound(ctx, c)
	code := "ok"
	if err != nil {
		code = err.Code
	}
	g.log.Info("call", "method", c.Method, "dir", "in", "code", code, "dur", time.Since(start).Round(time.Millisecond))
	if err != nil {
		return GwReply{}, remoteErr(err)
	}
	return GwReply{Payload: out}, nil
}

func (g *gateway) inbound(ctx context.Context, c GwCall) (json.RawMessage, *tetherapp.Error) {
	if !g.connected() {
		return nil, tetherapp.Errorf(tetherapp.CodeUnavailable, "handshake not complete")
	}
	if g.sc.calls[c.Method] != g.other {
		return nil, tetherapp.Errorf(tetherapp.CodePermissionDenied, "%s is not a method the %s side may call", c.Method, g.other)
	}
	m := g.sc.methods[c.Method]
	payload, e := g.validate(m, c.Payload, true)
	if e != nil {
		return nil, e
	}
	if g.appSocket == "" {
		return nil, tetherapp.Errorf(tetherapp.CodeUnavailable, "no app on the %s side", g.side)
	}
	actx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	resp, err := tetherapp.Post(actx, g.appSocket, "/"+c.Method, payload)
	if err != nil {
		var te *tetherapp.Error
		errors.As(err, &te)
		return nil, te
	}
	return g.validate(m, resp, false)
}

// outbound is the app-facing path.
func (g *gateway) outbound(ctx context.Context, name string, body []byte) ([]byte, *tetherapp.Error) {
	if g.sc.calls[name] != g.side {
		return nil, tetherapp.Errorf(tetherapp.CodePermissionDenied, "%s is not a method the %s side may call", name, g.side)
	}
	m := g.sc.methods[name]
	payload, e := g.validate(m, body, true)
	if e != nil {
		return nil, e
	}
	if !g.connected() {
		return nil, tetherapp.Errorf(tetherapp.CodeUnavailable, "not connected to the %s gateway", g.other)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, callTimeout)
		defer cancel()
	}
	r, err := tether.Call[GwCall, GwReply](ctx, g.peer, GwCall{Method: name, Payload: payload})
	if err != nil {
		var re *tether.RemoteError
		switch {
		case errors.As(err, &re):
			return nil, &tetherapp.Error{Code: re.Code, Message: re.Message}
		case errors.Is(err, tether.ErrNotConnected):
			return nil, tetherapp.Errorf(tetherapp.CodeUnavailable, "not connected to the %s gateway", g.other)
		case errors.Is(err, context.DeadlineExceeded):
			return nil, tetherapp.Errorf(tetherapp.CodeDeadlineExceeded, "call timed out")
		case errors.Is(err, context.Canceled):
			return nil, tetherapp.Errorf(tetherapp.CodeCanceled, "call canceled")
		}
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	return g.validate(m, r.Payload, false)
}

// ServeHTTP serves the gateway socket.
func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
		g.mu.Lock()
		h := map[string]any{
			"pair": g.sc.man.Pair, "version": g.sc.man.Version, "schema_sha256": g.sc.sha, "side": g.side,
			"connected": g.helloOK && g.peer != nil && g.peer.State() == tether.Open, "peer_version": g.peerVersion,
		}
		if g.reason != "" {
			h["reason"] = g.reason
		}
		g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(h)
		return
	}
	start := time.Now()
	name := strings.TrimPrefix(r.URL.Path, "/")
	out, err := g.app(w, r, name)
	code := "ok"
	if err != nil {
		code = err.Code
		tetherapp.WriteError(w, err)
	} else {
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}
	g.log.Info("call", "method", name, "dir", "out", "code", code, "dur", time.Since(start).Round(time.Millisecond))
}

func (g *gateway) app(w http.ResponseWriter, r *http.Request, name string) ([]byte, *tetherapp.Error) {
	if r.Method != http.MethodPost {
		return nil, tetherapp.Errorf(tetherapp.CodeUnimplemented, "use POST")
	}
	if _, ok := g.sc.methods[name]; !ok {
		// Unlisted and unknown methods look the same: not permitted.
		return nil, tetherapp.Errorf(tetherapp.CodePermissionDenied, "%s is not a method the %s side may call", name, g.side)
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "content-type must be application/json")
	}
	limit := min(g.sc.man.Limits.MaxMessage, tetherapp.MaxBody)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "read body: %v", err)
	}
	ctx := r.Context()
	if ms, err := strconv.ParseInt(r.Header.Get("Connect-Timeout-Ms"), 10, 64); err == nil && ms > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(ms)*time.Millisecond)
		defer cancel()
	}
	return g.outbound(ctx, name, body)
}
