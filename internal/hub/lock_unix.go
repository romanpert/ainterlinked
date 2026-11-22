//go:build !windows

package hub

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

// lockFile takes an exclusive OS lock on path without waiting. The kernel
// drops it when the process ends, however it ends, so a crash can't leave
// the hub locked. errLocked means another process holds it.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

// detach starts the hub process in its own session, so it outlives the
// session that happened to start it.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func startDetached(cmd *exec.Cmd) error { return cmd.Start() }

func noWindow(*exec.Cmd) {}
