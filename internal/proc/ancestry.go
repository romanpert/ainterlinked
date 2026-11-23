package proc

import (
	"errors"
	"runtime"
)

// AnchorOf is Anchor for another process. The hub calls it on the PID the
// kernel reports for a caller, so the identity a caller acts under comes
// from its real process tree, not from anything it says about itself.
func AnchorOf(pid int) (int, string) {
	if pid <= 0 {
		return 0, ""
	}
	if p, name, ok := anchorWalk(pid); ok {
		return p, name
	}
	table, err := snapshot()
	if err != nil {
		return 0, ""
	}
	return anchorFrom(table, pid)
}

// harnessNames are the executables of the agent harnesses: a caller whose
// anchor is one of them is an agent, never the human operator, even before
// it has registered.
var harnessNames = map[string]bool{
	"claude": true, "codex": true, "gemini": true, "opencode": true, "cursor-agent": true,
	"copilot": true, "aider": true, "goose": true, "amp": true, "qwen": true,
	"muse": true, "grok": true, "agy": true,
}

// IsHarness reports whether an anchor's executable name is an agent harness.
func IsHarness(name string) bool { return harnessNames[normName(name)] }

// Ancestry lists pid's ancestors, nearest first, from one snapshot of the
// process table. It decides who may act as the human, so it looks at the
// whole chain, not just the nearest program: an agent wrapping the CLI in
// node or anything else is still inside its harness. The walk ends where a
// process has no parent, or its parent no longer runs (on Windows, also
// where the parent's PID now belongs to a newer process). Whether that end
// is the login session or a break in the chain is Rooted's call. An error:
// pid isn't running, or the table loops or runs too deep to trust.
func Ancestry(pid int) ([]Info, error) {
	table, err := snapshot()
	if err != nil {
		return nil, err
	}
	return ancestryIn(table, pid)
}

func ancestryIn(table map[int]Info, pid int) ([]Info, error) {
	cur, ok := table[pid]
	if !ok {
		return nil, errNotRunning
	}
	var out []Info
	seen := map[int]bool{pid: true}
	for len(out) < 64 {
		if cur.PPID <= 0 {
			return out, nil
		}
		parent, found := table[cur.PPID]
		if !found || parent.Created > cur.Created {
			return out, nil // the parent has exited (and maybe its PID was reused)
		}
		if seen[parent.PID] {
			return nil, errLoop
		}
		seen[parent.PID] = true
		out = append(out, parent)
		cur = parent
	}
	return nil, errLoop
}

var (
	errNotRunning = errors.New("process not running")
	errLoop       = errors.New("the process table loops or runs too deep")
)

// Rooted reports whether a chain from Ancestry ends at the login session,
// so nothing between the caller and the session is missing. A chain that
// ends anywhere else is broken: on Windows, MSYS (Git Bash) exec's a
// program like timeout, env, xargs or a nested bash from a forked process
// that then exits, so the program's Windows parent is gone and the chain
// stops there, below the agent harness that ran it. Broken counts as
// unknown, and unknown is not the human. An IDE is broken the same way
// when its launcher exited (`code .` from a terminal) or its updater
// relaunched it, and so is every terminal inside it: the refusal says to
// reopen it from the Start menu.
//
// The Windows roots, from the process trees of a Windows 11 box on
// 2026-11-23:
//   - explorer: the user's shell (its parent, userinit, exits at logon).
//     What the Start menu, Run, the taskbar and Explorer launch hangs
//     under it: cmd, PowerShell, VS Code and the shells of its terminal;
//     a Windows program run from WSL hangs under wsl.exe, and on up.
//   - wininit: services.exe and svchost.exe hang under it, and so does
//     what svchost starts: packaged apps (seen: Claude's desktop app under
//     sihost; Windows Terminal is one), scheduled tasks, services.
//   - winlogon: the session's logon process; it restarts explorer after a
//     crash.
//
// On Unix an orphan is reparented to pid 1 or to a subreaper under it, so
// every chain the table shows in full ends at pid 1; one that doesn't hid
// a parent (a PID namespace, hidepid, a race) and is broken.
//
// Not a hard boundary: a process can leave its tree on purpose (WMI's
// Win32_Process.Create, schtasks, `explorer.exe prog`; setsid, a double
// fork or `nohup … &` that outlives its shell on Unix) and come out rooted.
// This catches the accidental break, not a deliberate escape.
func Rooted(chain []Info) bool { return rootedOn(runtime.GOOS, chain) }

var windowsRoots = map[string]bool{"explorer": true, "wininit": true, "winlogon": true}

func rootedOn(goos string, chain []Info) bool {
	if len(chain) == 0 {
		return false
	}
	top := chain[len(chain)-1]
	if goos == "windows" {
		return windowsRoots[top.Name]
	}
	return top.PID == 1
}
