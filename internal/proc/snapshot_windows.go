//go:build windows

package proc

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

// snapshot lists every process with its parent and creation time in one
// call. Windows reuses PIDs and never reparents, so a parent PID can name
// a newer, unrelated process; the creation time tells them apart.
func snapshot() (map[int]Info, error) {
	buf := make([]uint64, 64<<10) // 512 KiB, 8-byte aligned
	for try := 0; ; try++ {
		size := uint32(len(buf) * 8)
		var need uint32
		err := windows.NtQuerySystemInformation(windows.SystemProcessInformation, unsafe.Pointer(&buf[0]), size, &need)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.STATUS_INFO_LENGTH_MISMATCH) || try == 4 {
			return nil, err
		}
		if need < size {
			need = size
		}
		buf = make([]uint64, int(need)/8*2) // the table grows while we ask
	}
	out := map[int]Info{}
	base := unsafe.Pointer(&buf[0])
	for off := uintptr(0); ; {
		e := (*windows.SYSTEM_PROCESS_INFORMATION)(unsafe.Add(base, off))
		pid := int(e.UniqueProcessID)
		out[pid] = Info{PID: pid, PPID: int(e.InheritedFromUniqueProcessID), Name: normName(e.ImageName.String()), Created: e.CreateTime}
		if e.NextEntryOffset == 0 {
			return out, nil
		}
		off += uintptr(e.NextEntryOffset)
	}
}
