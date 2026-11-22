package proc

import "errors"

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
	"muse": true, "grok": true,
}

// IsHarness reports whether an anchor's executable name is an agent harness.
func IsHarness(name string) bool { return harnessNames[normName(name)] }

// Ancestry lists pid's ancestors, nearest first, from one snapshot of the
// process table. It decides who may act as the human, so it looks at the
// whole chain, not just the nearest program: an agent wrapping the CLI in
// node or anything else is still inside its harness. The walk ends at the
// root or where a parent no longer runs (on Windows every chain ends that
// way: explorer's parent has exited). An error: pid itself isn't running.
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
	for i := 0; i < 64; i++ {
		if cur.PPID <= 0 || seen[cur.PPID] {
			break
		}
		parent, found := table[cur.PPID]
		if !found {
			break
		}
		seen[parent.PID] = true
		out = append(out, parent)
		cur = parent
	}
	return out, nil
}

var errNotRunning = errors.New("process not running")
