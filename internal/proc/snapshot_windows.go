//go:build windows

package proc

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

func snapshot() (map[int]Info, error) {
	h, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	out := map[int]Info{}
	for err = windows.Process32First(h, &e); err == nil; err = windows.Process32Next(h, &e) {
		pid := int(e.ProcessID)
		out[pid] = Info{PID: pid, PPID: int(e.ParentProcessID), Name: normName(windows.UTF16ToString(e.ExeFile[:]))}
	}
	return out, nil
}
