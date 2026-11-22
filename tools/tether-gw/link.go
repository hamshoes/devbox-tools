package main

import "encoding/json"

// Messages on the gateway-to-gateway link.

type Hello struct {
	Pair         string `json:"pair"`
	Version      string `json:"version"`
	SchemaSHA256 string `json:"schema_sha256"`
}

func (Hello) MessageType() string { return "tether-gw.hello" }

type HelloReply struct {
	Pair         string `json:"pair"`
	Version      string `json:"version"`
	SchemaSHA256 string `json:"schema_sha256"`
}

func (HelloReply) MessageType() string { return "tether-gw.hello.reply" }

// GwCall carries one unary call; Payload is canonical protojson.
type GwCall struct {
	Method  string          `json:"method"` // "pkg.Service/Method"
	Payload json.RawMessage `json:"payload"`
	Caller  *Caller         `json:"caller,omitempty"` // set by the sending gateway; older peers omit it
	// TimeoutMs is what is left of the caller's deadline when the call is sent;
	// the receiving gateway gives its app that long (0: callTimeout).
	TimeoutMs int64 `json:"timeout_ms,omitempty"`
}

// Caller is the local process that made a call, from its socket's peer credentials.
type Caller struct {
	Side string `json:"side"`
	UID  uint32 `json:"uid"`
	PID  int32  `json:"pid"`
}

func (GwCall) MessageType() string { return "tether-gw.call" }

type GwReply struct {
	Payload json.RawMessage `json:"payload"`
}

func (GwReply) MessageType() string { return "tether-gw.call.reply" }
