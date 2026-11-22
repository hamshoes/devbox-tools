package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	clipv1 "github.com/hamshoes/devbox-tools/pairs/clip/gen/clip/v1"
	notifyv1 "github.com/hamshoes/devbox-tools/pairs/notify/gen/notify/v1"
	openv1 "github.com/hamshoes/devbox-tools/pairs/open/gen/open/v1"
	sendv1 "github.com/hamshoes/devbox-tools/pairs/send/gen/send/v1"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp"
	"github.com/hamshoes/devbox-tools/pkg/tetherapp/chunk"
	"google.golang.org/protobuf/proto"
)

const (
	// approvalWait covers a call that waits for Sam to answer a dialog on the
	// Mac; when it runs out the Mac withdraws the dialog and denies.
	approvalWait = 2 * time.Minute
	// pieceWait covers each later piece of a file.
	pieceWait = 30 * time.Second
	// maxClip matches clip-mac's limit.
	maxClip = 512 << 10
)

// pairCall calls method on the VM gateway of pair, with timeout.
func (e *env) pairCall(ctx context.Context, pair, method string, timeout time.Duration, req, resp proto.Message) error {
	sock := filepath.Join(e.cfg.TetherStateDir, pair, "gw.sock")
	if !socketExists(sock) {
		return fmt.Errorf("the %s pair is not running (no devbox session, or dev does not start it)", pair)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := tetherapp.Call(ctx, sock, method, req, resp)
	var te *tetherapp.Error
	if errors.As(err, &te) {
		switch te.Code {
		case tetherapp.CodePermissionDenied:
			return errors.New("denied on the Mac")
		case tetherapp.CodeUnavailable:
			return errors.New("no devbox session: the Mac is not connected")
		case tetherapp.CodeDeadlineExceeded:
			return errors.New("no answer in time; denied")
		}
	}
	return err
}

func openCmd(ctx context.Context, e *env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(e.stderr, "usage: bb open <url|file>")
		return 2
	}
	arg := args[0]
	if u, err := url.Parse(arg); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if err := e.pairCall(ctx, "open", "/open.v1.Open/OpenURL", approvalWait, &openv1.OpenURLRequest{Url: arg}, &openv1.OpenResponse{}); err != nil {
			return e.fail(err)
		}
		fmt.Fprintln(e.stdout, "opened on the Mac")
		return 0
	}
	if _, err := os.Stat(arg); err != nil {
		fmt.Fprintf(e.stderr, "bb: %s is neither an http(s) URL nor a file\n", arg)
		return 1
	}
	err := sendFile(ctx, e, arg, func(p chunk.Piece, timeout time.Duration) error {
		return e.pairCall(ctx, "open", "/open.v1.Open/OpenFile", timeout, openPiece(p), &openv1.OpenResponse{})
	})
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintln(e.stdout, "opened on the Mac")
	return 0
}

func sendCmd(ctx context.Context, e *env, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(e.stderr, "usage: bb send <file>")
		return 2
	}
	var saved string
	err := sendFile(ctx, e, args[0], func(p chunk.Piece, timeout time.Duration) error {
		var resp sendv1.SendResponse
		if err := e.pairCall(ctx, "send", "/send.v1.Send/Send", timeout, sendPiece(p), &resp); err != nil {
			return err
		}
		saved = resp.SavedAs
		return nil
	})
	if err != nil {
		return e.fail(err)
	}
	fmt.Fprintln(e.stdout, "saved on the Mac as", saved)
	return 0
}

// sendFile sends a regular file in pieces; the first piece waits for approval.
func sendFile(ctx context.Context, e *env, path string, send func(p chunk.Piece, timeout time.Duration) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	name, err := chunk.CleanName(filepath.Base(path))
	if err != nil {
		return err
	}
	return chunk.Send(f, name, fi.Size(), func(p chunk.Piece) error {
		timeout := pieceWait
		if p.Offset == 0 {
			timeout = approvalWait
		}
		return send(p, timeout)
	})
}

func openPiece(p chunk.Piece) *openv1.FilePiece {
	return &openv1.FilePiece{TransferId: p.ID, Name: p.Name, Size: p.Size, Offset: p.Offset, Data: p.Data, Last: p.Last, Sha256: p.SHA256}
}

func sendPiece(p chunk.Piece) *sendv1.FilePiece {
	return &sendv1.FilePiece{TransferId: p.ID, Name: p.Name, Size: p.Size, Offset: p.Offset, Data: p.Data, Last: p.Last, Sha256: p.SHA256}
}

func clipCmd(ctx context.Context, e *env, args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(e.stderr, "usage: <cmd> | bb clip")
		return 2
	}
	text, err := io.ReadAll(io.LimitReader(e.stdin, maxClip+1))
	if err != nil {
		return e.fail(err)
	}
	if len(text) > maxClip {
		fmt.Fprintf(e.stderr, "bb: input is over %s; use bb send\n", chunk.FormatSize(maxClip))
		return 1
	}
	if err := e.pairCall(ctx, "clip", "/clip.v1.Clip/Copy", approvalWait, &clipv1.CopyRequest{Text: string(text)}, &clipv1.CopyResponse{}); err != nil {
		return e.fail(err)
	}
	fmt.Fprintf(e.stdout, "copied %s to the Mac clipboard\n", chunk.FormatSize(int64(len(text))))
	return 0
}

func notifyCmd(ctx context.Context, e *env, args []string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(e.stderr, "usage: bb notify <title> [text]")
		return 2
	}
	req := &notifyv1.NotifyRequest{Title: args[0]}
	if len(args) == 2 {
		req.Text = args[1]
	}
	if strings.TrimSpace(req.Title) == "" {
		fmt.Fprintln(e.stderr, "bb: empty title")
		return 2
	}
	if err := e.pairCall(ctx, "notify", "/notify.v1.Notify/Notify", pieceWait, req, &notifyv1.NotifyResponse{}); err != nil {
		return e.fail(err)
	}
	return 0
}
