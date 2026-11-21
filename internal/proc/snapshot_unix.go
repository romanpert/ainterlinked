//go:build !windows

package proc

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

func snapshot() (map[int]Info, error) {
	if runtime.GOOS == "linux" {
		if t, err := procfs(); err == nil {
			return t, nil
		}
	}
	return psTable()
}

// procfs reads every /proc/<pid>/stat.
func procfs() (map[int]Info, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := map[int]Info{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if info, ok := procStat(pid); ok {
			out[pid] = info
		}
	}
	return out, nil
}

// procStat reads /proc/<pid>/stat: "pid (comm) state ppid ...".
func procStat(pid int) (Info, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return Info{}, false
	}
	open, close := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
	if open < 0 || close < open {
		return Info{}, false
	}
	fields := strings.Fields(string(b[close+1:]))
	if len(fields) < 2 {
		return Info{}, false
	}
	ppid, _ := strconv.Atoi(fields[1])
	return Info{PID: pid, PPID: ppid, Name: normName(string(b[open+1 : close]))}, true
}

// lookup reads one process without listing them all (Linux only; elsewhere
// the caller falls back to a full snapshot).
func lookup(pid int) (Info, bool) {
	if runtime.GOOS != "linux" {
		return Info{}, false
	}
	return procStat(pid)
}

// aliveOne reports whether pid is a running process (signal 0 probes it).
func aliveOne(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM), nil
}

// psTable uses ps(1) on macOS/BSD.
func psTable() (map[int]Info, error) {
	raw, err := exec.Command("ps", "-axo", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	out := map[int]Info{}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 != nil || e2 != nil {
			continue
		}
		out[pid] = Info{PID: pid, PPID: ppid, Name: normName(strings.Join(f[2:], " "))}
	}
	return out, nil
}
