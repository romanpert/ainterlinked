//go:build darwin

package machine

import (
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// readCPU estimates from the 1-minute load average: macOS has no CPU time
// counters without cgo. Runnable threads per core, capped at 100 %.
func readCPU(cores int) (cpuTimes, float64, error) {
	b, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return cpuTimes{}, -1, err
	}
	// struct loadavg { fixpt_t ldavg[3]; long fscale; } (fixpt_t = uint32)
	if len(b) < 24 || cores < 1 {
		return cpuTimes{}, -1, errors.New("vm.loadavg: short read")
	}
	load1 := binary.LittleEndian.Uint32(b[0:4])
	fscale := binary.LittleEndian.Uint64(b[16:24])
	if fscale == 0 {
		return cpuTimes{}, -1, errors.New("vm.loadavg: no scale")
	}
	return cpuTimes{}, 100 * float64(load1) / float64(fscale) / float64(cores), nil
}

func readMemory() (total, avail uint64, err error) {
	if total, err = unix.SysctlUint64("hw.memsize"); err != nil {
		return 0, 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return total, 0, nil // total known, available not
	}
	avail, err = parseVMStat(string(out))
	if err != nil {
		return total, 0, nil
	}
	return total, avail, nil
}

func readDisk(path string) (total, free uint64, err error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	return s.Blocks * uint64(s.Bsize), s.Bavail * uint64(s.Bsize), nil
}
