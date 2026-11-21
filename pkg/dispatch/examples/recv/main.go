// Command recv is a test client: it dials a dispatcher and prints every open_url Exec it receives.
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
	flag.Parse()

	token := os.Getenv(dispatch.TokenEnv)
	if token == "" {
		fmt.Fprintf(os.Stderr, "recv: %s is empty\n", dispatch.TokenEnv)
		os.Exit(1)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := tether.NewRouter(log)
	tether.On(router, func(_ context.Context, e dispatch.Exec) {
		args, err := dispatch.ParseOpenURL(e)
		if err != nil {
			fmt.Printf("rejected exec %s: %v\n", e.ID, err)
			return
		}
		fmt.Printf("exec %s open_url %s\n", e.ID, args.URL)
	})
	peer, err := tether.Dial(ctx, tether.ClientConfig{
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
