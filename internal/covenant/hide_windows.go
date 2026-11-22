package covenant

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hide keeps git from opening a console window: the hub has none of its own.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
