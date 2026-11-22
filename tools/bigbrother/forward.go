package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"google.golang.org/protobuf/proto"
)

// localTimeout bounds a call to controld; controld bounds its own call to the Mac below this.
const localTimeout = 20 * time.Second

// local calls a control.v1.Local method on controld's socket.
func (e *env) local(ctx context.Context, method string, req, resp proto.Message) error {
	ctx, cancel := context.WithTimeout(ctx, localTimeout)
	defer cancel()
	return tetherapp.Call(ctx, e.cfg.ControldSocket, "/control.v1.Local/"+method, req, resp)
}

func parsePort(s string) (uint32, error) {
	p, err := strconv.ParseUint(s, 10, 16)
	if err != nil || p == 0 {
		return 0, fmt.Errorf("%q is not a port", s)
	}
	return uint32(p), nil
}

func forwardCmd(ctx context.Context, e *env, args []string) int {
	fs := flag.NewFlagSet("forward", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	label := fs.String("label", "", "label shown in lists and notifications")
	port, ok := portArg(e, fs, args, "forward")
	if !ok {
		return 2
	}
	var f controlv1.Forward
	if err := e.local(ctx, "Expose", &controlv1.ExposeRequest{Port: port, Label: *label}, &f); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "forwarded %d: %s\n", f.VmPort, strings.Join(f.MacAddrs, ", "))
	return 0
}

func unforwardCmd(ctx context.Context, e *env, args []string) int {
	fs := flag.NewFlagSet("unforward", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	port, ok := portArg(e, fs, args, "unforward")
	if !ok {
		return 2
	}
	if err := e.local(ctx, "Unexpose", &controlv1.UnexposeRequest{Port: port}, &controlv1.UnexposeResponse{}); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "unforwarded %d\n", port)
	return 0
}

// portArg parses flags and the single port argument; flags may come before or after it.
func portArg(e *env, fs *flag.FlagSet, args []string, name string) (uint32, bool) {
	var rest []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return 0, false
		}
		args = fs.Args()
		if len(args) > 0 {
			rest = append(rest, args[0])
			args = args[1:]
		}
	}
	if len(rest) != 1 {
		fmt.Fprintf(e.stderr, "usage: bb %s <port>\n", name)
		return 0, false
	}
	port, err := parsePort(rest[0])
	if err != nil {
		fmt.Fprintln(e.stderr, "bb:", err)
		return 0, false
	}
	return port, true
}

func portsCmd(ctx context.Context, e *env, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(e.stderr, "usage: bb ports")
		return 2
	}
	var resp controlv1.ListForwardsResponse
	if err := e.local(ctx, "ListForwards", &controlv1.ListForwardsRequest{}, &resp); err != nil {
		return e.fail(err)
	}
	tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PORT\tSOURCE\tLABEL\tSTATE\tMAC")
	for _, f := range resp.Forwards {
		state := f.State
		if f.Error != "" {
			state += ": " + f.Error
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", f.VmPort, f.Source, f.Label, state, strings.Join(f.MacAddrs, ", "))
	}
	tw.Flush()
	return 0
}
