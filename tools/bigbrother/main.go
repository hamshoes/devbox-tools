// Command bigbrother (alias bb) is a short-lived client for the box's
// control plane and tether pairs. It serves nothing and keeps no state.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"

	"github.com/hamshoes/devbox-tools/pkg/platform"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
)

var version = "dev"

// command is one bb subcommand. run returns the process exit code.
type command struct {
	name, args, help string
	run              func(ctx context.Context, env *env, args []string) int
}

func commands() []command {
	return []command{
		{"forward", "<port> [--label L]", "forward a container port to the Mac as <box host>:<port>", forwardCmd},
		{"unforward", "<port>", "remove a forward made by bb forward or auto-forwarding", unforwardCmd},
		{"ports", "", "list the forwards the Mac holds this session", portsCmd},
		{"svc", "ls | enable <name> | disable <name>", "list or change the services controld runs", svcCmd},
		{"status", "[--json]", "what controld runs and what went wrong lately", statusCmd},
		{"open", "<url|file>", "open an http(s) URL or a file on the Mac, once allowed there", openCmd},
		{"clip", "", "copy stdin to the Mac clipboard, once allowed there", clipCmd},
		{"send", "<file>", "save a file to ~/Downloads/box/ on the Mac, once allowed there", sendCmd},
		{"notify", "<title> [text]", "show a notification on the Mac", notifyCmd},
	}
}

// env is what every command gets: where to talk and where to write.
type env struct {
	cfg            *platform.Config
	stdin          io.Reader
	stdout, stderr io.Writer
}

func main() {
	os.Exit(run())
}

func run() int {
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(os.Stdout)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "--version" || args[0] == "version" {
		fmt.Println(version)
		return 0
	}
	for _, c := range commands() {
		if c.name != args[0] {
			continue
		}
		if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
			fmt.Printf("usage: bb %s %s\n  %s\n", c.name, c.args, c.help)
			return 0
		}
		cfg, err := platform.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "bb:", err)
			return 1
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return c.run(ctx, &env{cfg: cfg, stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, args[1:])
	}
	fmt.Fprintf(os.Stderr, "bb: unknown command %q\n", args[0])
	usage(os.Stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: bb <command> [args]")
	fmt.Fprintln(w)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, c := range commands() {
		fmt.Fprintf(tw, "  %s %s\t%s\n", c.name, c.args, c.help)
	}
	tw.Flush()
}

// fail prints err as one line and returns exit code 1. A missing controld
// socket is reported as such rather than as a dial error.
func (e *env) fail(err error) int {
	var te *tetherapp.Error
	switch {
	case errors.As(err, &te) && te.Code == tetherapp.CodeUnavailable && !socketExists(e.cfg.ControldSocket):
		fmt.Fprintf(e.stderr, "bb: controld is not running (no socket at %s)\n", e.cfg.ControldSocket)
	case errors.As(err, &te) && te.Message != "":
		fmt.Fprintln(e.stderr, "bb:", te.Message)
	default:
		fmt.Fprintln(e.stderr, "bb:", err)
	}
	return 1
}

func socketExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode()&os.ModeSocket != 0
}
