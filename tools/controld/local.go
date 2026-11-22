package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

// localAPI serves control.v1.Local to bb on controld's unix socket (mode 0600).
// The caller of each call is identified by the socket's peer credentials.
type localAPI struct {
	log      *slog.Logger
	cp       *controlPlane
	svc      *serviceMgr
	problems *problems
}

type requesterKey struct{}

// requesterFrom is the process that opened the connection, or nil where peer credentials are unavailable.
func requesterFrom(ctx context.Context) *controlv1.Requester {
	rq, _ := ctx.Value(requesterKey{}).(*controlv1.Requester)
	return rq
}

func (a *localAPI) handler() http.Handler {
	s := tetherapp.NewServer()
	tetherapp.Handle(s, "/control.v1.Local/Expose", a.expose)
	tetherapp.Handle(s, "/control.v1.Local/Unexpose", a.unexpose)
	tetherapp.Handle(s, "/control.v1.Local/ListForwards", a.listForwards)
	tetherapp.Handle(s, "/control.v1.Local/ListServices", a.listServices)
	tetherapp.Handle(s, "/control.v1.Local/EnableService", a.enableService)
	tetherapp.Handle(s, "/control.v1.Local/DisableService", a.disableService)
	tetherapp.Handle(s, "/control.v1.Local/Status", a.status)
	return s
}

// serve listens on path until ctx ends.
func (a *localAPI) serve(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	ln, err := tetherapp.ListenUnix(path)
	if err != nil {
		return err
	}
	defer os.Remove(path)
	srv := &http.Server{
		Handler:           a.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, requesterKey{}, requesterOf(c))
		},
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func (a *localAPI) expose(ctx context.Context, r *controlv1.ExposeRequest) (*controlv1.Forward, error) {
	rq := requesterFrom(ctx)
	f, err := a.cp.expose(ctx, r.Port, r.Label, rq)
	a.log.Info("expose", "port", r.Port, "requester_pid", rq.GetPid(), "err", err)
	return f, err
}

func (a *localAPI) unexpose(ctx context.Context, r *controlv1.UnexposeRequest) (*controlv1.UnexposeResponse, error) {
	rq := requesterFrom(ctx)
	err := a.cp.unexpose(ctx, r.Port, rq)
	a.log.Info("unexpose", "port", r.Port, "requester_pid", rq.GetPid(), "err", err)
	if err != nil {
		return nil, err
	}
	return &controlv1.UnexposeResponse{}, nil
}

func (a *localAPI) listForwards(ctx context.Context, _ *controlv1.ListForwardsRequest) (*controlv1.ListForwardsResponse, error) {
	return a.cp.list(ctx)
}

func (a *localAPI) listServices(context.Context, *controlv1.ListServicesRequest) (*controlv1.ListServicesResponse, error) {
	return &controlv1.ListServicesResponse{Services: a.svc.list()}, nil
}

func (a *localAPI) enableService(ctx context.Context, r *controlv1.ServiceRequest) (*controlv1.ServiceStatus, error) {
	st, err := a.svc.setEnabled(r.Name, true)
	a.log.Info("service enable", "service", r.Name, "requester_pid", requesterFrom(ctx).GetPid(), "err", err)
	return st, err
}

func (a *localAPI) disableService(ctx context.Context, r *controlv1.ServiceRequest) (*controlv1.ServiceStatus, error) {
	st, err := a.svc.setEnabled(r.Name, false)
	a.log.Info("service disable", "service", r.Name, "requester_pid", requesterFrom(ctx).GetPid(), "err", err)
	return st, err
}

func (a *localAPI) status(ctx context.Context, _ *controlv1.StatusRequest) (*controlv1.StatusResponse, error) {
	return buildStatus(ctx, a.cp, a.svc, a.problems), nil
}

// buildStatus is what controld runs and what went wrong lately; it works with no session.
func buildStatus(ctx context.Context, cp *controlPlane, svc *serviceMgr, pr *problems) *controlv1.StatusResponse {
	st := &controlv1.StatusResponse{Version: version, ControlRunning: cp != nil, Services: svc.list(), Problems: pr.recent()}
	if cp != nil {
		st.Session = gwConnected(ctx, cp.gwSock)
		st.Pairs = cp.pairs.results()
	}
	return st
}
