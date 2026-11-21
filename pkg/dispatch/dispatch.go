// Package dispatch defines the exec events the devbox dispatcher pushes to the Mac,
// and the local socket protocol its CLI speaks.
package dispatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"
	"unicode"
)

const (
	DispatcherPort = 4005  // container, tether server
	MacDialPort    = 14005 // Mac side of the ssh forward
	MacExecPort    = 14006 // Mac executor
	TetherPath     = "/tether"
	TokenEnv       = "DISPATCH_TOKEN"
)

const (
	ActionOpenURL = "open_url"
	// ActionStatus is a local socket request only; it is never sent to the Mac.
	ActionStatus = "status"
)

const maxURLLen = 2048

// Exec is the only event type for now. It names an action; it never carries a command line.
type Exec struct {
	ID     string          `json:"id"`
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
	At     time.Time       `json:"at"`
}

func (Exec) MessageType() string { return "exec" }

type OpenURLArgs struct {
	URL string `json:"url"`
}

// NewOpenURL validates rawURL and builds an open_url Exec with a new ID.
func NewOpenURL(rawURL string) (Exec, error) {
	args := OpenURLArgs{URL: rawURL}
	if err := args.validate(); err != nil {
		return Exec{}, err
	}
	data, err := json.Marshal(args)
	if err != nil {
		return Exec{}, err
	}
	id, err := newID()
	if err != nil {
		return Exec{}, err
	}
	return Exec{ID: id, Action: ActionOpenURL, Args: data, At: time.Now().UTC()}, nil
}

// ParseOpenURL decodes and validates the args of an open_url Exec.
func ParseOpenURL(e Exec) (OpenURLArgs, error) {
	if e.Action != ActionOpenURL {
		return OpenURLArgs{}, fmt.Errorf("dispatch: action %q is not %q", e.Action, ActionOpenURL)
	}
	var args OpenURLArgs
	if err := json.Unmarshal(e.Args, &args); err != nil {
		return OpenURLArgs{}, fmt.Errorf("dispatch: bad open_url args: %w", err)
	}
	if err := args.validate(); err != nil {
		return OpenURLArgs{}, err
	}
	return args, nil
}

func (a OpenURLArgs) validate() error {
	s := a.URL
	switch {
	case s == "":
		return errors.New("dispatch: empty url")
	case len(s) > maxURLLen:
		return fmt.Errorf("dispatch: url longer than %d bytes", maxURLLen)
	case s[0] == '-':
		return errors.New("dispatch: url must not start with '-'")
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return errors.New("dispatch: url contains control or whitespace characters")
		}
	}
	u, err := url.Parse(s)
	if err != nil {
		return errors.New("dispatch: url does not parse")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("dispatch: url scheme must be http or https")
	}
	if u.Host == "" {
		return errors.New("dispatch: url has no host")
	}
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Request and Response are the local socket protocol between the CLI and the
// dispatcher: one JSON object per line each way. For a status request, Delivered
// reports whether the Mac is connected.
type Request struct {
	Action string          `json:"action"`
	Args   json.RawMessage `json:"args"`
}

type Response struct {
	OK        bool   `json:"ok"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error,omitempty"`
	ID        string `json:"id,omitempty"`
	// Result carries the payload of expose, unexpose and ports.
	Result json.RawMessage `json:"result,omitempty"`
}

func SocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/dispatch/dispatch.sock")
}

// RedactURL returns scheme://host/… for logging. Never log full URLs, since
// login URLs often carry codes or tokens.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "<invalid url>"
	}
	return u.Scheme + "://" + u.Host + "/…"
}
