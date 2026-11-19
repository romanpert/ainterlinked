// Package proc finds the "anchor" process of the current harness session.
//
// A harness (Claude Code, Codex, ...) spawns both its MCP servers and its hook
// commands. Hooks usually run through a shell (bash -c, cmd /c), MCP servers
// sometimes through a launcher (uvx, npx). Walking up the process tree and
// skipping those wrappers yields the same PID from both sides, which lets the
// hook process and the MCP process agree on a single agent identity.
package proc

import (
	"os"
	"path/filepath"
	"strings"
)

// wrappers are executables skipped while walking up to the harness.
var wrappers = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"cmd": true, "powershell": true, "pwsh": true, "conhost": true, "env": true,
	"timeout": true, "uv": true, "uvx": true, "python": true, "python3": true, "py": true,
	"npx": true, "npm": true, "interlinked": true, "nohup": true, "sudo": true,
	"mintty": true, "winpty": true, "git-bash": true, "busybox": true,
}

// Info is one process in the tree.
type Info struct {
	PID  int
	PPID int
	Name string // lowercased executable base name without extension
}

func normName(n string) string {
	n = strings.ToLower(filepath.Base(n))
	return strings.TrimSuffix(n, ".exe")
}

// IsWrapper reports whether name is a shell or launcher to skip.
func IsWrapper(name string) bool { return wrappers[normName(name)] }

// Anchor returns the nearest ancestor of the current process that is not a
// shell/launcher, and its name. It returns (0, "") if the tree can't be read.
// INTERLINKED_ANCHOR_PID overrides detection (tests, exotic setups).
func Anchor() (int, string) {
	if v := os.Getenv("INTERLINKED_ANCHOR_PID"); v != "" {
		var pid int
		for _, c := range v {
			if c < '0' || c > '9' {
				return 0, ""
			}
			pid = pid*10 + int(c-'0')
		}
		return pid, "override"
	}
	table, err := snapshot()
	if err != nil {
		return 0, ""
	}
	return anchorFrom(table, os.Getpid())
}

func anchorFrom(table map[int]Info, self int) (int, string) {
	cur, ok := table[self]
	if !ok {
		return 0, ""
	}
	seen := map[int]bool{self: true}
	for i := 0; i < 32; i++ {
		parent, ok := table[cur.PPID]
		if !ok || seen[parent.PID] || parent.PID == 0 {
			// Parent unknown (exited or not visible): fall back to the raw PPID.
			return cur.PPID, ""
		}
		seen[parent.PID] = true
		if !wrappers[parent.Name] {
			return parent.PID, parent.Name
		}
		cur = parent
	}
	return cur.PPID, ""
}

// Alive reports whether a process with this PID exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	table, err := snapshot()
	if err != nil {
		return true // unknown: assume alive, heartbeats decide
	}
	_, ok := table[pid]
	return ok
}
