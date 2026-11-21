package dispatch

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"time"
)

const (
	ActionForwardPort   = "forward_port"   // VM -> Mac exec
	ActionForwardCancel = "forward_cancel" // VM -> Mac exec
	MaxForwards         = 10
)

// Local socket actions only; never sent to the Mac.
const (
	ActionExpose   = "expose"
	ActionUnexpose = "unexpose"
	ActionPorts    = "ports"
)

// Forward states, as reported by ports and expose.
const (
	StatePending = "pending"
	StateActive  = "active"
	StateWaiting = "waiting"
	StateFailed  = "failed"
)

// ReservedPorts can never be forwarded: sshd and the dispatcher's own channels.
var ReservedPorts = []int{22, 4005, 14005, 14006}

var forwardNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type ForwardPortArgs struct {
	Name string `json:"name"`
	Host string `json:"host"` // container IP
	Port int    `json:"port"` // container port
}

type ForwardCancelArgs struct {
	Name string `json:"name"`
}

// ForwardReady is the Mac's reply to a forward_port Exec. ID echoes the Exec.ID being answered.
type ForwardReady struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	VMPort  int    `json:"vm_port"`
	MacPort int    `json:"mac_port"`
}

func (ForwardReady) MessageType() string { return "forward_ready" }

// ForwardFailed is the Mac's negative reply to a forward_port Exec.
type ForwardFailed struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Error string `json:"error"`
}

func (ForwardFailed) MessageType() string { return "forward_failed" }

// ValidateForwardName checks a forward name.
func ValidateForwardName(name string) error {
	if !forwardNameRe.MatchString(name) {
		return fmt.Errorf("dispatch: name %q must match %s", name, forwardNameRe)
	}
	return nil
}

// ValidateForwardHost accepts only private IPv4 addresses. Loopback, link-local
// (including the 169.254.169.254 metadata address) and public addresses are rejected.
func ValidateForwardHost(host string) error {
	a, err := netip.ParseAddr(host)
	if err != nil || !a.Is4() {
		return fmt.Errorf("dispatch: host %q is not an IPv4 address", host)
	}
	if !a.IsPrivate() {
		return fmt.Errorf("dispatch: host %q is not a private address", host)
	}
	return nil
}

// ValidateForwardPort accepts 1024-65535 outside ReservedPorts.
func ValidateForwardPort(port int) error {
	switch {
	case slices.Contains(ReservedPorts, port):
		return fmt.Errorf("dispatch: port %d is reserved", port)
	case port < 1024 || port > 65535:
		return fmt.Errorf("dispatch: port %d outside 1024-65535", port)
	}
	return nil
}

func (a ForwardPortArgs) validate() error {
	if err := ValidateForwardName(a.Name); err != nil {
		return err
	}
	if err := ValidateForwardHost(a.Host); err != nil {
		return err
	}
	return ValidateForwardPort(a.Port)
}

// NewForwardPort validates its arguments and builds a forward_port Exec with a new ID.
func NewForwardPort(name, host string, port int) (Exec, error) {
	args := ForwardPortArgs{Name: name, Host: host, Port: port}
	if err := args.validate(); err != nil {
		return Exec{}, err
	}
	return newExec(ActionForwardPort, args)
}

// ParseForwardPort decodes and validates the args of a forward_port Exec.
func ParseForwardPort(e Exec) (ForwardPortArgs, error) {
	var args ForwardPortArgs
	if err := parseExec(e, ActionForwardPort, &args); err != nil {
		return ForwardPortArgs{}, err
	}
	if err := args.validate(); err != nil {
		return ForwardPortArgs{}, err
	}
	return args, nil
}

// NewForwardCancel validates name and builds a forward_cancel Exec with a new ID.
func NewForwardCancel(name string) (Exec, error) {
	if err := ValidateForwardName(name); err != nil {
		return Exec{}, err
	}
	return newExec(ActionForwardCancel, ForwardCancelArgs{Name: name})
}

// ParseForwardCancel decodes and validates the args of a forward_cancel Exec.
func ParseForwardCancel(e Exec) (ForwardCancelArgs, error) {
	var args ForwardCancelArgs
	if err := parseExec(e, ActionForwardCancel, &args); err != nil {
		return ForwardCancelArgs{}, err
	}
	if err := ValidateForwardName(args.Name); err != nil {
		return ForwardCancelArgs{}, err
	}
	return args, nil
}

func newExec(action string, args any) (Exec, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return Exec{}, err
	}
	id, err := newID()
	if err != nil {
		return Exec{}, err
	}
	return Exec{ID: id, Action: action, Args: data, At: time.Now().UTC()}, nil
}

func parseExec(e Exec, action string, into any) error {
	if e.Action != action {
		return fmt.Errorf("dispatch: action %q is not %q", e.Action, action)
	}
	if err := json.Unmarshal(e.Args, into); err != nil {
		return fmt.Errorf("dispatch: bad %s args: %w", action, err)
	}
	return nil
}

// Socket protocol payloads (CLI <-> dispatcher). Request.Args carries the args
// types; Response.Result carries ExposeResult or []PortInfo.

type ExposeArgs struct {
	Name string `json:"name"`
	Port int    `json:"port"`
	Open bool   `json:"open"`
}

// UnexposeArgs names the forward to remove by Name or, if Name is empty, by Port.
type UnexposeArgs struct {
	Name string `json:"name,omitempty"`
	Port int    `json:"port,omitempty"`
}

type ExposeResult struct {
	Name    string `json:"name"`
	VMPort  int    `json:"vm_port"`
	MacPort int    `json:"mac_port"`
	State   string `json:"state"`
}

type PortInfo struct {
	Name    string    `json:"name"`
	VMPort  int       `json:"vm_port"`
	MacPort int       `json:"mac_port"`
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Error   string    `json:"error"`
}
