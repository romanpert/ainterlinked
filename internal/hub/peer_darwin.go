//go:build darwin

package hub

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID asks the kernel which process is on the other end of a local
// socket (LOCAL_PEERPID). The caller can't choose the answer.
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var optErr error
	if err := raw.Control(func(fd uintptr) {
		pid, optErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	return pid, optErr
}
