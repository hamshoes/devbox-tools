package main

import (
	"net"
	"syscall"

	"github.com/hamshoes/devbox-tools/pkg/dispatch"
)

// requesterOf identifies the process on the other end of a local dispatcher socket connection.
func requesterOf(c net.Conn) *dispatch.Requester {
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
	return &dispatch.Requester{UID: cred.Uid, PID: cred.Pid, Cmd: procCmd(cred.Pid)}
}
