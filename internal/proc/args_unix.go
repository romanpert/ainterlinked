//go:build !windows

package proc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Args returns the command line pid was started with, the program first.
// Where only ps(1) tells, an argument with spaces comes split.
func Args(pid int) ([]string, error) {
	if runtime.GOOS == "linux" {
		b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return nil, err
		}
		if len(b) == 0 {
			return nil, errors.New("no command line")
		}
		return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00"), nil
	}
	b, err := exec.Command("ps", "-o", "args=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(b)), nil
}
