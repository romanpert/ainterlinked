package proc

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Args returns the command line pid was started with, the program first.
func Args(pid int) ([]string, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	// The answer is a UNICODE_STRING with its text behind it; a command line
	// is at most 64 KB.
	buf := make([]uint64, (64<<10+64)/8)
	if err := windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation,
		unsafe.Pointer(&buf[0]), uint32(len(buf)*8), nil); err != nil {
		return nil, err
	}
	s := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0])).String()
	if s == "" {
		return nil, errors.New("no command line")
	}
	return windows.DecomposeCommandLine(s)
}
