//go:build linux

package hub

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID asks the kernel which process is on the other end of a local
// socket (SO_PEERCRED). The caller can't choose the answer.
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			credErr = err
			return
		}
		pid = int(cred.Pid)
	}); err != nil {
		return 0, err
	}
	return pid, credErr
}
