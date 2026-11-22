package main

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCred reads the uid and pid of the process on the other end of c.
func peerCred(c net.Conn) (uid uint32, pid int32, ok bool) {
	uc, isUnix := c.(*net.UnixConn)
	if !isUnix {
		return 0, 0, false
	}
	rc, err := uc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	rc.Control(func(fd uintptr) {
		x, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err != nil {
			return
		}
		p, err := unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
		if err != nil {
			return
		}
		uid, pid, ok = x.Uid, int32(p), true
	})
	return
}
