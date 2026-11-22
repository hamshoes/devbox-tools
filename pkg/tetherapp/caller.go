package tetherapp

import (
	"context"
	"net/http"
	"strconv"
)

// Caller is the local process that made a call, as its gateway saw it on the
// unix socket. It is for attribution, not authorisation.
type Caller struct {
	Side string // "vm" or "mac": the side the process runs on
	UID  uint32
	PID  int32
}

const (
	HeaderCallerSide = "Tether-Caller-Side"
	HeaderCallerUID  = "Tether-Caller-Uid"
	HeaderCallerPID  = "Tether-Caller-Pid"
)

type callerKey struct{}

// CallerFrom returns the caller of the call being handled. It is false when
// the peer gateway sent none (an older gateway).
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// StripCaller removes every Tether-Caller-* header. Only gateways set them.
func StripCaller(h http.Header) {
	for k := range h {
		if len(k) >= 14 && http.CanonicalHeaderKey(k)[:14] == "Tether-Caller-" {
			delete(h, k)
		}
	}
}

// SetCaller sets the caller headers on h.
func SetCaller(h http.Header, c Caller) {
	h.Set(HeaderCallerSide, c.Side)
	h.Set(HeaderCallerUID, strconv.FormatUint(uint64(c.UID), 10))
	h.Set(HeaderCallerPID, strconv.FormatInt(int64(c.PID), 10))
}

func callerFromHeader(h http.Header) (Caller, bool) {
	side := h.Get(HeaderCallerSide)
	uid, err1 := strconv.ParseUint(h.Get(HeaderCallerUID), 10, 32)
	pid, err2 := strconv.ParseInt(h.Get(HeaderCallerPID), 10, 32)
	if (side != "vm" && side != "mac") || err1 != nil || err2 != nil {
		return Caller{}, false
	}
	return Caller{Side: side, UID: uint32(uid), PID: int32(pid)}, true
}
