package proc

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
}

// IsHarness reports whether an anchor's executable name is an agent harness.
func IsHarness(name string) bool { return harnessNames[normName(name)] }
