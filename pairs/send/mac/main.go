// Command send-mac is the Mac half of the send pair: it saves files the VM
// sends under ~/Downloads/devbox/, each only after Sam allows it in a notify
// dialog, and never replaces an existing file.
// The dialog comes from notify-mac's approval socket, NOTIFY_APPROVE_SOCKET
// (read by package approve); without it every request is denied.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/hamshoes/devbox-tools/pairs/notify/approve"
	sendv1 "github.com/hamshoes/devbox-tools/pairs/send/gen/send/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
)

var version = "dev"

const maxFileSize = 1 << 30

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "send-mac:", err)
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
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	s := &sender{log: log, dir: filepath.Join(home, "Downloads", "devbox")}
	s.recv = &chunk.Receiver{
		// Inside the target folder, so finished files are linked, not copied, into place.
		Dir: filepath.Join(s.dir, ".partial"), MaxSize: maxFileSize,
		Approve: func(ctx context.Context, name string, size int64) (bool, error) {
			return approve.Ask(ctx, "Save a file from the VM?",
				fmt.Sprintf("%s (%s)\n\nSaved to ~/Downloads/devbox/", name, chunk.FormatSize(size)))
		},
	}
	app := tetherapp.NewServer()
	tetherapp.Handle(app, "/send.v1.Send/Send", s.send)
	return app.Serve(ctx, appSock)
}

type sender struct {
	log  *slog.Logger
	dir  string
	recv *chunk.Receiver
}

func (s *sender) send(ctx context.Context, r *sendv1.FilePiece) (*sendv1.SendResponse, error) {
	done, err := s.recv.Put(ctx, chunk.Piece{
		ID: r.TransferId, Name: r.Name, Size: r.Size, Offset: r.Offset, Data: r.Data, Last: r.Last, SHA256: r.Sha256,
	})
	if err != nil {
		return nil, err
	}
	if done == nil {
		return &sendv1.SendResponse{}, nil
	}
	path, err := chunk.Keep(done.Path, s.dir, done.Name)
	if err != nil {
		os.Remove(done.Path)
		return nil, tetherapp.Errorf(tetherapp.CodeInternal, "%v", err)
	}
	s.log.Info("file saved", "path", path, "size", done.Size)
	return &sendv1.SendResponse{SavedAs: path}, nil
}
