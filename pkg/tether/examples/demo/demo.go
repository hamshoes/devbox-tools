// Package demo holds the message types shared by tetherping and tetherlog.
package demo

import "time"

// RequestSeen reports one HTTP request handled by tetherping.
type RequestSeen struct {
	Method string    `json:"method"`
	Route  string    `json:"route"`
	Status int       `json:"status"`
	At     time.Time `json:"at"`
}

func (RequestSeen) MessageType() string { return "demo.request_seen" }
