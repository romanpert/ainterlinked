//go:build windows

package hub

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioAfUnixGetPeerPID is SIO_AF_UNIX_GETPEERPID (_WSAIOR(IOC_VENDOR, 256)),
// Windows 10 1809 and later.
const sioAfUnixGetPeerPID = 0x58000100

// peerPID asks the kernel which process is on the other end of a local
// socket. The caller can't choose the answer.
func peerPID(c *net.UnixConn) (int, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid uint32
	var ioErr error
	if err := raw.Control(func(fd uintptr) {
		var n uint32
		ioErr = windows.WSAIoctl(windows.Handle(fd), sioAfUnixGetPeerPID, nil, 0,
			(*byte)(unsafe.Pointer(&pid)), uint32(unsafe.Sizeof(pid)), &n, nil, 0)
	}); err != nil {
		return 0, err
	}
	return int(pid), ioErr
}
