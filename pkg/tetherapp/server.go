package tetherapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// DefaultTimeout applies to a call that carries no Connect-Timeout-Ms.
const DefaultTimeout = 30 * time.Second

// Server serves the calls a gateway forwards to this app.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]http.HandlerFunc
}

func NewServer() *Server { return &Server{handlers: map[string]http.HandlerFunc{}} }

// Handle registers fn for method (e.g. "/ping.v1.PingControl/Ping"). Req and
// Resp are the generated pointer types. An *Error returned by fn travels with
// its code; any other error is "internal".
func Handle[Req, Resp proto.Message](s *Server, method string, fn func(ctx context.Context, req Req) (Resp, error)) {
	method = normalize(method)
	h := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, Errorf(CodeUnimplemented, "use POST"))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			WriteError(w, Errorf(CodeInvalidArgument, "content-type must be application/json"))
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
		if err != nil {
			WriteError(w, Errorf(CodeInvalidArgument, "read body: %v", err))
			return
		}
		var zero Req
		req := zero.ProtoReflect().New().Interface().(Req)
		if err := protojson.Unmarshal(body, req); err != nil {
			WriteError(w, Errorf(CodeInvalidArgument, "%v", err))
			return
		}
		ctx, cancel := requestContext(r)
		defer cancel()
		if c, ok := callerFromHeader(r.Header); ok {
			ctx = context.WithValue(ctx, callerKey{}, c)
		}
		resp, err := fn(ctx, req)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				e = Errorf(CodeInternal, "%v", err)
			}
			WriteError(w, e)
			return
		}
		out, err := protojson.Marshal(resp)
		if err != nil {
			WriteError(w, Errorf(CodeInternal, "encode response: %v", err))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = h
}

// requestContext applies Connect-Timeout-Ms, or DefaultTimeout without it.
func requestContext(r *http.Request) (context.Context, context.CancelFunc) {
	d := DefaultTimeout
	if ms, err := strconv.ParseInt(r.Header.Get("Connect-Timeout-Ms"), 10, 64); err == nil && ms > 0 {
		d = time.Duration(ms) * time.Millisecond
	}
	return context.WithTimeout(r.Context(), d)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	h := s.handlers[r.URL.Path]
	s.mu.RUnlock()
	if h == nil {
		WriteError(w, Errorf(CodeUnimplemented, "no such method %s", r.URL.Path))
		return
	}
	h(w, r)
}

// Serve listens on the unix socket appSocket (mode 0600) until ctx ends.
func (s *Server) Serve(ctx context.Context, appSocket string) error {
	ln, err := ListenUnix(appSocket)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if srv.Shutdown(sctx) != nil {
			srv.Close()
		}
	}()
	err = srv.Serve(ln)
	os.Remove(appSocket)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ListenUnix replaces any stale socket at path with a fresh listener, mode 0600.
func ListenUnix(path string) (net.Listener, error) {
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// FromEnv reads the contract a pair app is started with.
func FromEnv() (gwSocket, appSocket string, err error) {
	gwSocket, appSocket = os.Getenv("TETHER_GW_SOCKET"), os.Getenv("TETHER_APP_SOCKET")
	if gwSocket == "" || appSocket == "" {
		return "", "", fmt.Errorf("tetherapp: TETHER_GW_SOCKET and TETHER_APP_SOCKET must both be set")
	}
	return gwSocket, appSocket, nil
}
