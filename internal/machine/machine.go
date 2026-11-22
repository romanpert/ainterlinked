// Package machine reads this machine's load: CPU, memory and the disk the
// hub lives on. It reads global counters only (what a task manager shows),
// never other processes, their memory or their command lines.
package machine

import (
	"bufio"
	"errors"
	"runtime"
	"strconv"
	"strings"
)

// Sample is one reading. CPU is a percentage of all cores, or -1 while
// unknown (the first reading only primes the counters).
type Sample struct {
	CPU       float64 `json:"cpu"`
	MemTotal  uint64  `json:"mem_total"`
	MemAvail  uint64  `json:"mem_avail"`
	DiskTotal uint64  `json:"disk_total"`
	DiskFree  uint64  `json:"disk_free"`
	Cores     int     `json:"cores"`
	// Approx marks a CPU figure estimated from the load average (macOS),
	// not measured from CPU time.
	Approx bool `json:"approx,omitempty"`
}

// ErrUnsupported means this OS has no reader.
var ErrUnsupported = errors.New("machine load is not read on this OS")

// readCPU (per OS) returns cumulative counters, or, where only a load
// average exists, an estimated percentage in est (est < 0 otherwise).

// cpuTimes are cumulative CPU counters: busy and total, in any unit.
type cpuTimes struct{ busy, total uint64 }

// Sampler reads samples; CPU is the share busy since the previous Read.
type Sampler struct {
	// Path is a directory on the disk to report (the hub's).
	Path string
	prev cpuTimes
	have bool
}

// Read takes one sample. Parts that fail to read are left zero.
func (s *Sampler) Read() (Sample, error) {
	out := Sample{CPU: -1, Cores: runtime.NumCPU()}
	var errs []error
	if t, est, err := readCPU(out.Cores); err != nil {
		errs = append(errs, err)
	} else if est >= 0 {
		out.CPU, out.Approx = clampPct(est), true
	} else {
		if s.have && t.total > s.prev.total && t.busy >= s.prev.busy {
			out.CPU = clampPct(100 * float64(t.busy-s.prev.busy) / float64(t.total-s.prev.total))
		}
		s.prev, s.have = t, true
	}
	if total, avail, err := readMemory(); err != nil {
		errs = append(errs, err)
	} else {
		out.MemTotal, out.MemAvail = total, avail
	}
	if s.Path != "" {
		if total, free, err := readDisk(s.Path); err != nil {
			errs = append(errs, err)
		} else {
			out.DiskTotal, out.DiskFree = total, free
		}
	}
	if len(errs) == 3 || (s.Path == "" && len(errs) == 2) {
		return out, errors.Join(errs...)
	}
	return out, nil
}

func clampPct(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 100:
		return 100
	}
	return v
}

// parseProcStat reads the aggregate "cpu" line of Linux /proc/stat:
// user nice system idle iowait irq softirq steal …; idle and iowait are
// the idle part. Guest time is already inside user and nice.
func parseProcStat(data string) (cpuTimes, error) {
	for _, line := range strings.Split(data, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || f[0] != "cpu" {
			continue
		}
		var t cpuTimes
		for i, v := range f[1:] {
			if i >= 8 { // guest, guest_nice: counted in user/nice already
				break
			}
			n, err := strconv.ParseUint(v, 10, 64)
			if err != nil {
				return cpuTimes{}, err
			}
			t.total += n
			if i != 3 && i != 4 { // idle, iowait
				t.busy += n
			}
		}
		return t, nil
	}
	return cpuTimes{}, errors.New("/proc/stat: no cpu line")
}

// parseMeminfo reads MemTotal and MemAvailable (kB) from Linux /proc/meminfo.
func parseMeminfo(data string) (total, avail uint64, err error) {
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 2 {
			continue
		}
		n, perr := strconv.ParseUint(f[1], 10, 64)
		if perr != nil {
			continue
		}
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	if total == 0 {
		return 0, 0, errors.New("/proc/meminfo: no MemTotal")
	}
	return total, avail, nil
}

// parseVMStat reads macOS vm_stat output: free, inactive and speculative
// pages can be handed out without paging, which is what "available" means.
func parseVMStat(data string) (uint64, error) {
	page := uint64(4096)
	var pages uint64
	found := false
	for _, line := range strings.Split(data, "\n") {
		if i := strings.Index(line, "page size of "); i >= 0 {
			if n, err := strconv.ParseUint(strings.Fields(line[i+len("page size of "):])[0], 10, 64); err == nil {
				page = n
			}
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "Pages free", "Pages inactive", "Pages speculative":
			n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(v), "."), 10, 64)
			if err == nil {
				pages += n
				found = true
			}
		}
	}
	if !found {
		return 0, errors.New("vm_stat: no page counts")
	}
	return pages * page, nil
}
