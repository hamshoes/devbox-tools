// Command open-mac is the Mac half of the open pair: it opens URLs and files
// the VM sends, each only after Sam allows it in a notify dialog.
// The dialog comes from notify-mac's approval socket, NOTIFY_APPROVE_SOCKET
// (read by package approve); without it every request is denied.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"unicode"

	"github.com/hamshoes/devbox-tools/pairs/notify/approve"
	openv1 "github.com/hamshoes/devbox-tools/pairs/open/gen/open/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

var version = "dev"

const (
	maxURL      = 2048
	maxFileSize = 100 << 20
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "open-mac:", err)
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
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	o := &opener{log: log, dir: filepath.Join(cache, "devbox", "open")}
	o.recv = &chunk.Receiver{
		Dir: filepath.Join(o.dir, ".partial"), MaxSize: maxFileSize,
		Approve: func(ctx context.Context, name string, size int64) (bool, error) {
			return approve.Ask(ctx, "Open a file from the VM?",
				fmt.Sprintf("%s (%s)\n\nIt opens with its default app on this Mac.", name, chunk.FormatSize(size)))
		},
	}
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/open.v1.Open/OpenURL", o.openURL)
	tetherapp.Handle(app, "/open.v1.Open/OpenFile", o.openFile)
	return app.Serve(ctx, appSock)
}

type opener struct {
	log  *slog.Logger
	dir  string
	recv *chunk.Receiver
}

func (o *opener) openURL(ctx context.Context, r *openv1.OpenURLRequest) (*openv1.OpenResponse, error) {
	u, err := checkURL(r.Url)
	if err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInvalidArgument, "%v", err)
	}
	ok, err := approve.Ask(ctx, "Open a URL from the VM?", u)
	if err != nil || !ok {
		o.log.Info("url denied", "err", err)
		return nil, tetherapp.Errorf(tetherapp.CodePermissionDenied, "denied on the Mac")
	}
	if err := exec.CommandContext(ctx, "open", u).Run(); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "open: %v", err)
	}
	o.log.Info("url opened")
	return &openv1.OpenResponse{}, nil
}

// checkURL accepts only absolute http and https URLs with no control or invisible characters.
func checkURL(s string) (string, error) {
	if len(s) > maxURL {
		return "", fmt.Errorf("url is longer than %d characters", maxURL)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("url has control, invisible or space characters")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("only http and https URLs open")
	}
	return u.String(), nil
}

func (o *opener) openFile(ctx context.Context, r *openv1.FilePiece) (*openv1.OpenResponse, error) {
	done, err := o.recv.Put(ctx, chunk.Piece{
		ID: r.TransferId, Name: r.Name, Size: r.Size, Offset: r.Offset, Data: r.Data, Last: r.Last, SHA256: r.Sha256,
	})
	if err != nil {
		return nil, err
	}
	if done == nil {
		return &openv1.OpenResponse{}, nil
	}
	// Each file gets its own folder so its name stays as sent.
	dir := filepath.Join(o.dir, r.TransferId)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		os.Remove(done.Path)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	path, err := chunk.Keep(done.Path, dir, done.Name)
	if err != nil {
		os.Remove(done.Path)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	if err := exec.CommandContext(ctx, "open", path).Run(); err != nil {
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "open: %v", err)
	}
	o.log.Info("file opened", "path", path, "size", done.Size)
	return &openv1.OpenResponse{}, nil
}
