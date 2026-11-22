//go:build !linux && !darwin

package main

import "net"

func peerCred(net.Conn) (uint32, int32, bool) { return 0, 0, false }
