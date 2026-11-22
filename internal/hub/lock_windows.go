//go:build windows

package hub

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// lockFile takes an exclusive OS lock on path without waiting. The kernel
// drops it when the process ends, however it ends, so a crash can't leave
// the hub locked. errLocked means another process holds it.
func lockFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	ol := new(windows.Overlapped)
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ol)
	if err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
			return nil, errLocked
		}
		return nil, err
	}
	return f, nil
}

// detach starts the hub process on its own: no console, not killed with
// the session that happened to start it.
// A harness may run its hooks in a job object that kills every child when
// the hook ends; the hub process asks to leave it (CREATE_BREAKAWAY_FROM_JOB)
// and, if the job forbids that, starts inside it (startDetached retries).
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_BREAKAWAY_FROM_JOB,
		HideWindow:    true,
	}
}

// startDetached starts cmd, without breakaway if the job doesn't allow it.
func startDetached(cmd *exec.Cmd) error {
	err := cmd.Start()
	if err != nil && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		cmd.SysProcAttr.CreationFlags &^= windows.CREATE_BREAKAWAY_FROM_JOB
		cmd.Process = nil
		return cmd.Start()
	}
	return err
}

// noWindow runs a console program without opening a window for it.
func noWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW, HideWindow: true}
}
