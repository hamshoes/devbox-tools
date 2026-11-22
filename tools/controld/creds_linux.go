package main

import (
	"net"
	"syscall"

	controlv1 "github.com/hamshoes/devbox-tools/pairs/control/gen/control/v1"
)

// requesterOf identifies the process on the other end of a local socket connection, from SO_PEERCRED.
func requesterOf(c net.Conn) *controlv1.Requester {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return nil
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return nil
	}
	var cred *syscall.Ucred
	rc.Control(func(fd uintptr) {
		cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cred == nil {
		return nil
	}
	return &controlv1.Requester{Uid: cred.Uid, Pid: cred.Pid, Cmd: procCmd(cred.Pid)}
}
