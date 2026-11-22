package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
	"github.com/hamshoes/devbox-tools/pkg/platform"
	"google.golang.org/protobuf/encoding/protojson"
)

// svcUsage is the bb svc help; it names the client from the platform.
func svcUsage() string {
	return `usage: bb svc <command>
  ls               list every service, its source (config, manifest, bb) and state
  enable <name>    enable a manifest tool as a service, or re-enable one
  disable <name>   stop a service and keep it stopped
Services named in the config of ` + platform.ClientName() + ` are changed there, not here.`
}

func svcCmd(ctx context.Context, e *env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(e.stderr, svcUsage())
		return 2
	}
	switch sub, rest := args[0], args[1:]; {
	case sub == "-h" || sub == "--help" || sub == "help":
		fmt.Fprintln(e.stdout, svcUsage())
		return 0
	case sub == "ls" && len(rest) == 0:
		var resp controlv1.ListServicesResponse
		if err := e.local(ctx, "ListServices", &controlv1.ListServicesRequest{}, &resp); err != nil {
			return e.fail(err)
		}
		printServices(e.stdout, resp.Services)
		return 0
	case (sub == "enable" || sub == "disable") && len(rest) == 1:
		method := "EnableService"
		if sub == "disable" {
			method = "DisableService"
		}
		var st controlv1.ServiceStatus
		if err := e.local(ctx, method, &controlv1.ServiceRequest{Name: rest[0]}, &st); err != nil {
			return e.fail(err)
		}
		printServices(e.stdout, []*controlv1.ServiceStatus{&st})
		return 0
	}
	fmt.Fprintln(e.stderr, svcUsage())
	return 2
}

func printServices(w io.Writer, list []*controlv1.ServiceStatus) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tSTATE\tSINCE\tCOMMAND")
	for _, s := range list {
		state := s.State
		if s.Error != "" {
			state += ": " + s.Error
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Source, state, s.Since, strings.Join(append([]string{s.Tool}, s.Args...), " "))
	}
	tw.Flush()
}

func statusCmd(ctx context.Context, e *env, args []string) int {
	asJSON := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !asJSON {
		fmt.Fprintln(e.stderr, "usage: bb status [--json]")
		return 2
	}
	var st controlv1.StatusResponse
	if err := e.local(ctx, "Status", &controlv1.StatusRequest{}, &st); err != nil {
		return e.fail(err)
	}
	if asJSON {
		out, err := protojson.MarshalOptions{EmitUnpopulated: true, UseProtoNames: true}.Marshal(&st)
		if err != nil {
			return e.fail(err)
		}
		fmt.Fprintln(e.stdout, string(out))
		return 0
	}
	fmt.Fprintf(e.stdout, "controld %s; control plane running: %v; session: %v\n", st.Version, st.ControlRunning, st.Session)
	fmt.Fprintln(e.stdout)
	printServices(e.stdout, st.Services)
	if len(st.Pairs) > 0 {
		fmt.Fprintln(e.stdout)
		tw := tabwriter.NewWriter(e.stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PAIR\tVERSION\tPORT\tSTATE")
		for _, p := range st.Pairs {
			state := p.State
			if p.Error != "" {
				state += ": " + p.Error
			}
			fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", p.Name, p.Version, p.VmPort, state)
		}
		tw.Flush()
	}
	if len(st.Problems) > 0 {
		fmt.Fprintln(e.stdout, "\nrecent problems:")
		for _, p := range st.Problems {
			fmt.Fprintf(e.stdout, "  %s %s: %s\n", p.At, p.Source, p.Message)
		}
	}
	return 0
}
