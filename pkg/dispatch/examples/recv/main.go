// Command recv is a test client: it dials a dispatcher and prints every Exec it receives.
// With -forward it also answers forward_port, standing in for the Mac.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
	"github.com/hamshoes/devbox-tools/pkg/tether"
)

func main() {
	port := flag.Int("port", dispatch.DispatcherPort, "dispatcher port on 127.0.0.1")
	forward := flag.Bool("forward", false, "answer forward_port with forward_ready on port+10000")
	flag.Parse()

	token := os.Getenv(dispatch.TokenEnv)
	if token == "" {
		fmt.Fprintf(os.Stderr, "recv: %s is empty\n", dispatch.TokenEnv)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var peer tether.Peer
	router := tether.NewRouter(log)
	tether.On(router, func(ctx context.Context, e dispatch.Exec) {
		switch e.Action {
		case dispatch.ActionForwardPort:
			args, err := dispatch.ParseForwardPort(e)
			if err != nil {
				fmt.Printf("rejected exec %s: %v\n", e.ID, err)
				return
			}
			fmt.Printf("exec %s forward_port %s %s:%d\n", e.ID, args.Name, args.Host, args.Port)
			if !*forward {
				return
			}
			ready := dispatch.ForwardReady{ID: e.ID, Name: args.Name, VMPort: args.Port, MacPort: args.Port + 10000}
			if err := peer.Send(ctx, ready); err != nil {
				fmt.Printf("forward_ready %s: %v\n", args.Name, err)
			}
		case dispatch.ActionForwardCancel:
			args, err := dispatch.ParseForwardCancel(e)
			if err != nil {
				fmt.Printf("rejected exec %s: %v\n", e.ID, err)
				return
			}
			fmt.Printf("exec %s forward_cancel %s\n", e.ID, args.Name)
		default:
			args, err := dispatch.ParseOpenURL(e)
			if err != nil {
				fmt.Printf("rejected exec %s: %v\n", e.ID, err)
				return
			}
			fmt.Printf("exec %s open_url %s\n", e.ID, args.URL)
		}
	})
	var err error
	peer, err = tether.Dial(ctx, tether.ClientConfig{
		URL:   fmt.Sprintf("ws://127.0.0.1:%d%s", *port, dispatch.TetherPath),
		Token: token,
	}, router, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "recv:", err)
		os.Exit(1)
	}
	defer peer.Close()
	<-ctx.Done()
}
