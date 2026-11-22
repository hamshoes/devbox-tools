package dispatch

// Requester is the local process that asked, as the dispatcher saw it on its socket.
type Requester struct {
	UID uint32 `json:"uid"`
	PID int32  `json:"pid"`
	Cmd string `json:"cmd"`
}

// ForwardInfo is one forward held by the Mac, as listed by the control plane.
type ForwardInfo struct {
	VMPort    int        `json:"vm_port"`
	MacPort   int        `json:"mac_port"`
	Addrs     []string   `json:"addrs"`
	Label     string     `json:"label"`
	Source    string     `json:"source"` // "config" | "expose" | "auto"
	Requester *Requester `json:"requester,omitempty"`
	CreatedAt string     `json:"created_at"`
	State     string     `json:"state"` // "active" | "failed"
	Error     string     `json:"error,omitempty"`
}

// ErrControlDown is the Response.Error for requests that need the control plane when it is not connected.
const ErrControlDown = "control plane not connected"
