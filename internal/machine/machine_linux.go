//go:build linux

package machine

import (
	"os"

	"golang.org/x/sys/unix"
)

func readCPU(int) (cpuTimes, float64, error) {
	b, err := os.ReadFile("/proc/stat")
	if err != nil {
		return cpuTimes{}, -1, err
	}
	t, err := parseProcStat(string(b))
	return t, -1, err
}

func readMemory() (total, avail uint64, err error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseMeminfo(string(b))
}

func readDisk(path string) (total, free uint64, err error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return 0, 0, err
	}
	return s.Blocks * uint64(s.Bsize), s.Bavail * uint64(s.Bsize), nil
}
