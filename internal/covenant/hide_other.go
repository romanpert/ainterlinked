//go:build !windows

package covenant

import "os/exec"

func hide(*exec.Cmd) {}
