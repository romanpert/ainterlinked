//go:build !windows && !linux && !darwin

package hub

import (
	"errors"
	"net"
)

// peerPID is unknown here: such systems run the hub in-process (Local).
func peerPID(*net.UnixConn) (int, error) {
	return 0, errors.New("this system can't tell who is on the other end of a local socket")
}
