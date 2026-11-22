package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/dispatch"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const forwardsTimeout = 15 * time.Second

// controlPlane is the dispatcher's view of the control gateway, for the calls
// that go to the Mac. A nil *controlPlane is a control plane that is not running.
type controlPlane struct {
	cfg    *platform.Config
	gwSock string
	host   func() (string, error) // container IP the Mac forwards to
}

func (c *controlPlane) up(ctx context.Context) bool {
	return c != nil && gwConnected(ctx, c.gwSock)
}

func toRequester(r *dispatch.Requester) *controlv1.Requester {
	if r == nil {
		return nil
	}
	return &controlv1.Requester{Uid: r.UID, Pid: r.PID, Cmd: r.Cmd}
}

func fromRequester(r *controlv1.Requester) *dispatch.Requester {
	if r == nil {
		return nil
	}
	return &dispatch.Requester{UID: r.Uid, PID: r.Pid, Cmd: r.Cmd}
}

// checkPort rejects ports the Mac would refuse, so the CLI can say why at once.
func (c *controlPlane) checkPort(p int) error {
	switch {
	case c.cfg.IsReserved(p):
		return fmt.Errorf("port %d is reserved", p)
	case p < 1024 || p > 65535:
		return fmt.Errorf("port %d is outside 1024-65535", p)
	}
	return nil
}

func (c *controlPlane) expose(ctx context.Context, a dispatch.ExposeArgs, rq *dispatch.Requester) (dispatch.ExposeResult, error) {
	if err := c.checkPort(a.Port); err != nil {
		return dispatch.ExposeResult{}, &tetherapp.Error{Code: tetherapp.CodeInvalidArgument, Message: err.Error()}
	}
	host, err := c.host()
	if err != nil {
		return dispatch.ExposeResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	var f controlv1.Forward
	err = tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/Expose",
		&controlv1.ExposeRequest{Port: uint32(a.Port), TargetHost: host, Label: a.Label, Requester: toRequester(rq)}, &f)
	if err != nil {
		return dispatch.ExposeResult{}, err
	}
	return dispatch.ExposeResult{
		Name: a.Name, VMPort: int(f.VmPort), MacPort: int(f.MacPort), State: f.State,
		Addrs: f.MacAddrs, Label: f.Label,
	}, nil
}

func (c *controlPlane) unexpose(ctx context.Context, port int, rq *dispatch.Requester) error {
	if err := c.checkPort(port); err != nil {
		return &tetherapp.Error{Code: tetherapp.CodeInvalidArgument, Message: err.Error()}
	}
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	return tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/Unexpose",
		&controlv1.UnexposeRequest{Port: uint32(port), Requester: toRequester(rq)}, &controlv1.UnexposeResponse{})
}

func (c *controlPlane) list(ctx context.Context) ([]dispatch.ForwardInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	var resp controlv1.ListForwardsResponse
	if err := tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/ListForwards", &controlv1.ListForwardsRequest{}, &resp); err != nil {
		return nil, err
	}
	out := make([]dispatch.ForwardInfo, 0, len(resp.Forwards))
	for _, f := range resp.Forwards {
		out = append(out, dispatch.ForwardInfo{
			VMPort: int(f.VmPort), MacPort: int(f.MacPort), Addrs: f.MacAddrs, Label: f.Label, Source: f.Source,
			Requester: fromRequester(f.Requester), CreatedAt: f.CreatedAt, State: f.State, Error: f.Error,
		})
	}
	return out, nil
}

// controlUnavailable reports whether err says the control plane could not be reached.
func controlUnavailable(err error) bool {
	var te *tetherapp.Error
	return errors.As(err, &te) && te.Code == tetherapp.CodeUnavailable
}

// controlMessage turns a control call's error into one plain line for the CLI.
func (c *controlPlane) controlMessage(err error) string {
	var te *tetherapp.Error
	if !errors.As(err, &te) {
		return err.Error()
	}
	switch {
	case te.Code == tetherapp.CodeUnavailable:
		return dispatch.ErrControlDown
	case te.Code == tetherapp.CodeResourceExhausted && te.Message == "":
		return fmt.Sprintf("too many forwards (%d)", c.cfg.MaxForwards)
	case te.Message != "":
		return te.Message
	}
	return te.Code
}
