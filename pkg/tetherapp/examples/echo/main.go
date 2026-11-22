// Command echo is a test app for tether-gw: it serves Echo on its app socket,
// and with -call makes one call through its gateway and prints the result.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	echov1 "github.com/hamshoes/devbox-tools/pkg/tetherapp/examples/echo/gen/echo/v1"
)

func logCaller(ctx context.Context, who string) {
	c, ok := tetherapp.CallerFrom(ctx)
	fmt.Fprintf(os.Stderr, "echo %s handler: caller=%+v ok=%v\n", who, c, ok)
}

func main() {
	side := flag.String("side", "", "vm or mac: which side this app runs on")
	call := flag.String("call", "", "make one call with this text, print the result, exit")
	method := flag.String("method", "", "method to call (default: this side's Echo)")
	flag.Parse()

	gw, app, err := tetherapp.FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if *call != "" || *method != "" {
		m := *method
		if m == "" {
			m = map[string]string{"vm": "/echo.v1.EchoMac/Echo", "mac": "/echo.v1.EchoVM/Echo"}[*side]
		}
		var resp echov1.EchoResponse
		if err := tetherapp.Call(ctx, gw, m, &echov1.EchoRequest{Text: *call}, &resp); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Println(resp.Text)
		return
	}

	s := tetherapp.NewServer()
	tetherapp.Handle(s, "/echo.v1.EchoVM/Echo", func(ctx context.Context, r *echov1.EchoRequest) (*echov1.EchoResponse, error) {
		logCaller(ctx, "vm")
		return &echov1.EchoResponse{Text: "vm:" + r.Text}, nil
	})
	tetherapp.Handle(s, "/echo.v1.EchoMac/Echo", func(ctx context.Context, r *echov1.EchoRequest) (*echov1.EchoResponse, error) {
		logCaller(ctx, "mac")
		return &echov1.EchoResponse{Text: "mac:" + r.Text}, nil
	})
	if err := s.Serve(ctx, app); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
