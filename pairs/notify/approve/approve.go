// Package approve is how a Mac pair app asks Sam before acting: it calls
// notify-mac's approval socket, which shows a Deny-by-default dialog (one at a
// time, queued with the VM's asks) and answers allow or deny.
//
// dev sets NOTIFY_APPROVE_SOCKET for every Mac pair app. The wire format is
// POST /approve with {"title","body"}, answered by {"allow"}. An unreachable
// socket, a closed connection or an expired deadline is a deny.
package approve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// EnvSocket names the environment variable holding the socket path.
const EnvSocket = "NOTIFY_APPROVE_SOCKET"

// Path is the one route the socket serves.
const Path = "/approve"

// MaxRequest bounds a request body, in bytes.
const MaxRequest = 16 << 10

// DefaultWait bounds an Ask whose context has no deadline.
const DefaultWait = 2 * time.Minute

// Request is the dialog to show. notify-mac cleans and shortens both fields.
type Request struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Response is the decision.
type Response struct {
	Allow bool `json:"allow"`
}

// Ask shows a dialog and reports whether Sam chose Allow. It returns false
// with an error when no decision could be had; callers treat that as a deny.
func Ask(ctx context.Context, title, body string) (bool, error) {
	sock := os.Getenv(EnvSocket)
	if sock == "" {
		return false, fmt.Errorf("approve: %s is not set", EnvSocket)
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultWait)
		defer cancel()
	}
	raw, err := json.Marshal(Request{Title: title, Body: body})
	if err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://notify"+Path, bytes.NewReader(raw))
	if err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}}
	resp, err := hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("approve: http %d", resp.StatusCode)
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<10)).Decode(&out); err != nil {
		return false, fmt.Errorf("approve: %w", err)
	}
	return out.Allow, nil
}
