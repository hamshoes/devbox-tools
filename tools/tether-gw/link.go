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
}

func (GwCall) MessageType() string { return "tether-gw.call" }

type GwReply struct {
	Payload json.RawMessage `json:"payload"`
}

func (GwReply) MessageType() string { return "tether-gw.call.reply" }
