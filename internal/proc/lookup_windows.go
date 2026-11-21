//go:build windows

package proc

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stillActive = 259 // STILL_ACTIVE exit code

// lookup reads one process's parent and executable name without taking a
// snapshot of the whole process table (which costs ~100 ms on a busy box).
func lookup(pid int) (Info, bool) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return Info{}, false
	}
	defer windows.CloseHandle(h)
	var pbi windows.PROCESS_BASIC_INFORMATION
	if err := windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation,
		unsafe.Pointer(&pbi), uint32(unsafe.Sizeof(pbi)), nil); err != nil {
		return Info{}, false
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return Info{}, false
	}
	return Info{PID: pid, PPID: int(pbi.InheritedFromUniqueProcessId), Name: normName(windows.UTF16ToString(buf[:n]))}, true
}

// aliveOne reports whether pid is a running process.
func aliveOne(pid int) (bool, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	switch {
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return true, nil // exists, just not ours to open
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return false, nil // no such process
	case err != nil:
		return false, err
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	return code == stillActive, nil
}
