//go:build !linux

package main

import (
	"net"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
)

// requesterOf needs SO_PEERCRED, which only Linux has; elsewhere calls are unattributed.
func requesterOf(net.Conn) *controlv1.Requester { return nil }
