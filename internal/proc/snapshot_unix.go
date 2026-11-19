//go:build !windows

package proc

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func snapshot() (map[int]Info, error) {
	if runtime.GOOS == "linux" {
		if t, err := procfs(); err == nil {
			return t, nil
		}
	}
	return psTable()
}

// procfs reads /proc/<pid>/stat: "pid (comm) state ppid ...".
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
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		open, close := bytes.IndexByte(b, '('), bytes.LastIndexByte(b, ')')
		if open < 0 || close < open {
			continue
		}
		fields := strings.Fields(string(b[close+1:]))
		if len(fields) < 2 {
			continue
		}
		ppid, _ := strconv.Atoi(fields[1])
		out[pid] = Info{PID: pid, PPID: ppid, Name: normName(string(b[open+1 : close]))}
	}
	return out, nil
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
