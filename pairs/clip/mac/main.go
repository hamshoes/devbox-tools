// Command clip-mac is the Mac half of the clip pair: it puts text from the VM
// on the clipboard, only after Sam allows it in a notify dialog.
// The dialog comes from notify-mac's approval socket, NOTIFY_APPROVE_SOCKET
// (read by package approve); without it every request is denied.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"unicode/utf8"

	clipv1 "github.com/hamshoes/devbox-tools/pairs/clip/gen/clip/v1"
	"github.com/hamshoes/devbox-tools/pairs/notify/approve"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

var version = "dev"

const (
	maxText = 512 << 10
	preview = 300 // runes of the text shown in the dialog
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "clip-mac:", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return nil
	}
	_, appSock, err := tetherapp.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/clip.v1.Clip/Copy", func(ctx context.Context, r *clipv1.CopyRequest) (*clipv1.CopyResponse, error) {
		return copyText(ctx, log, r.Text)
	})
	return app.Serve(ctx, appSock)
}

func copyText(ctx context.Context, log *slog.Logger, text string) (*clipv1.CopyResponse, error) {
	switch {
	case len(text) > maxText:
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "text is over %s", chunk.FormatSize(maxText))
	case !utf8.ValidString(text):
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "text is not UTF-8")
	}
	shown := text
	if r := []rune(shown); len(r) > preview {
		shown = string(r[:preview]) + "…"
	}
	lines := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		lines++
	}
	body := fmt.Sprintf("%s, %d lines\n\n%s", chunk.FormatSize(int64(len(text))), lines, shown)
	ok, err := approve.Ask(ctx, "Copy to the Mac clipboard?", body)
	if err != nil || !ok {
		log.Info("clip denied", "bytes", len(text), "err", err)
		return nil, tetherapp.Errorf(tetherapp.CodePermissionDenied, "denied on the Mac")
	}
	cmd := exec.CommandContext(ctx, "pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "pbcopy: %v", err)
	}
	// Clipboard content is never logged, only its size.
	log.Info("clip copied", "bytes", len(text))
	return &clipv1.CopyResponse{}, nil
}
