//go:build !windows && !linux && !darwin

package machine

func readCPU(int) (cpuTimes, float64, error)          { return cpuTimes{}, -1, ErrUnsupported }
func readMemory() (total, avail uint64, err error)    { return 0, 0, ErrUnsupported }
func readDisk(string) (total, free uint64, err error) { return 0, 0, ErrUnsupported }
