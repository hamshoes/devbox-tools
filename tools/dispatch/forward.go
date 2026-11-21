package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
)

func exposeUsage() int {
	fmt.Fprintln(os.Stderr, "usage: dispatch expose <port> [--name <n>] [--open]")
	return exitInvalid
}

func expose(argv []string) int {
	var args dispatch.ExposeArgs
	for i := 0; i < len(argv); i++ {
		switch a := argv[i]; {
		case a == "--open":
			args.Open = true
		case a == "--name":
			if i++; i >= len(argv) {
				return exposeUsage()
			}
			args.Name = argv[i]
		case strings.HasPrefix(a, "--name="):
			args.Name = strings.TrimPrefix(a, "--name=")
		case args.Port == 0:
			p, err := strconv.Atoi(a)
			if err != nil {
				return exposeUsage()
			}
			args.Port = p
		default:
			return exposeUsage()
		}
	}
	if args.Port == 0 {
		return exposeUsage()
	}
	var res dispatch.ExposeResult
	if code := callResult(dispatch.ActionExpose, args, &res); code != 0 {
		return code
	}
	switch res.State {
	case dispatch.StateActive:
		fmt.Printf("%s → http://localhost:%d\n", res.Name, res.MacPort)
	case dispatch.StateWaiting:
		fmt.Printf("%s: waiting for mac — forwards when dev connects\n", res.Name)
	default:
		fmt.Printf("%s: %s — see dispatch ports\n", res.Name, res.State)
	}
	return 0
}

func unexpose(argv []string) int {
	if len(argv) != 1 {
		fmt.Fprintln(os.Stderr, "usage: dispatch unexpose <name|port>")
		return exitInvalid
	}
	args := dispatch.UnexposeArgs{Name: argv[0]}
	if p, err := strconv.Atoi(argv[0]); err == nil {
		args = dispatch.UnexposeArgs{Port: p}
	}
	if code := callResult(dispatch.ActionUnexpose, args, nil); code != 0 {
		return code
	}
	fmt.Println("removed", argv[0])
	return 0
}

func ports() int {
	var list []dispatch.PortInfo
	if code := callResult(dispatch.ActionPorts, struct{}{}, &list); code != 0 {
		return code
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVM PORT\tMAC URL\tSTATE\tSINCE\tERROR")
	for _, p := range list {
		url := "-"
		if p.State == dispatch.StateActive && p.MacPort > 0 {
			url = fmt.Sprintf("http://localhost:%d", p.MacPort)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", p.Name, p.VMPort, url, p.State, since(p.Since), p.Error)
	}
	tw.Flush()
	return 0
}

func since(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// callResult sends one request and decodes the response's Result into out (if non-nil).
// It prints any failure and returns the exit code.
func callResult(action string, args any, out any) int {
	data, err := json.Marshal(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatch:", err)
		return exitUnreachable
	}
	resp, err := call(dispatch.Request{Action: action, Args: data})
	switch {
	case err != nil:
		fmt.Fprintln(os.Stderr, "dispatch: dispatcher unreachable:", err)
		return exitUnreachable
	case !resp.OK:
		fmt.Fprintln(os.Stderr, "dispatch:", resp.Error)
		return exitUnreachable
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			fmt.Fprintln(os.Stderr, "dispatch: bad response:", err)
			return exitUnreachable
		}
	}
	return 0
}
