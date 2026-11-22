package main

import (
	"context"
	"errors"
	"sync"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

const forwardsTimeout = 15 * time.Second

// controlPlane is controld's view of the control gateway, for the calls that
// go to the Mac. A nil *controlPlane is a control plane that is not running.
type controlPlane struct {
	cfg    *platform.Config
	gwSock string
	pairs  *pairMgr

	mu sync.Mutex
	ip string // container IP the Mac forwards to; looked up again while empty
}

var errNoControl = tetherapp.Errorf(tetherapp.CodeUnavailable, "control plane is not running on the VM (tether-gw or the control pair is not installed)")

// errNoSession is returned when the control gateway has no Mac peer.
var errNoSession = tetherapp.Errorf(tetherapp.CodeUnavailable, "no devbox session: the Mac is not connected")

// ready reports why calls to the Mac can't be made now, or nil.
func (c *controlPlane) ready(ctx context.Context) error {
	switch {
	case c == nil:
		return errNoControl
	case !gwConnected(ctx, c.gwSock):
		return errNoSession
	}
	return nil
}

func (c *controlPlane) hostIP() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ip == "" {
		ip, err := containerIP()
		if err != nil {
			return "", err
		}
		c.ip = ip
	}
	return c.ip, nil
}

// checkPort rejects ports the Mac would refuse, so bb can say why at once.
func (c *controlPlane) checkPort(p uint32) error {
	switch {
	case c.cfg.IsReserved(int(p)):
		return tetherapp.Errorf(tetherapp.CodeInvalidArgument, "port %d is reserved (%s)", p, c.cfg.ReservedString())
	case p < 1024 || p > 65535:
		return tetherapp.Errorf(tetherapp.CodeInvalidArgument, "port %d is outside 1024-65535", p)
	}
	return nil
}

// expose asks the Mac to forward a container port, on behalf of rq.
func (c *controlPlane) expose(ctx context.Context, port uint32, label string, rq *controlv1.Requester) (*controlv1.Forward, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	if err := c.checkPort(port); err != nil {
		return nil, err
	}
	host, err := c.hostIP()
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "container ip: %v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	var f controlv1.Forward
	err = tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/Expose",
		&controlv1.ExposeRequest{Port: port, TargetHost: host, Label: label, Requester: rq}, &f)
	if err != nil {
		return nil, c.mapErr(err)
	}
	return &f, nil
}

func (c *controlPlane) unexpose(ctx context.Context, port uint32, rq *controlv1.Requester) error {
	if err := c.ready(ctx); err != nil {
		return err
	}
	if err := c.checkPort(port); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	err := tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/Unexpose",
		&controlv1.UnexposeRequest{Port: port, Requester: rq}, &controlv1.UnexposeResponse{})
	return c.mapErr(err)
}

func (c *controlPlane) list(ctx context.Context) (*controlv1.ListForwardsResponse, error) {
	if err := c.ready(ctx); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, forwardsTimeout)
	defer cancel()
	var resp controlv1.ListForwardsResponse
	if err := tetherapp.Call(ctx, c.gwSock, "/control.v1.Forwards/ListForwards", &controlv1.ListForwardsRequest{}, &resp); err != nil {
		return nil, c.mapErr(err)
	}
	return &resp, nil
}

// mapErr gives the Mac's errors a plain message where it sent none.
func (c *controlPlane) mapErr(err error) error {
	var te *tetherapp.Error
	switch {
	case !errors.As(err, &te):
		return err
	case te.Code == tetherapp.CodeUnavailable:
		return errNoSession
	case te.Code == tetherapp.CodeResourceExhausted && te.Message == "":
		return tetherapp.Errorf(te.Code, "too many forwards (%d)", c.cfg.MaxForwards)
	}
	return err
}
