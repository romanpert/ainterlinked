package proc

import (
	"os"
	"testing"
)

func TestAnchorSkipsShellsAndLaunchers(t *testing.T) {
	// claude.exe → bash.exe → interlinked.exe (hook)
	// claude.exe → interlinked.exe (MCP server)
	// codex → uvx → python → interlinked (MCP via uvx)
	table := map[int]Info{
		1:  {PID: 1, PPID: 0, Name: "explorer"},
		10: {PID: 10, PPID: 1, Name: "claude"},
		11: {PID: 11, PPID: 10, Name: "bash"},
		12: {PID: 12, PPID: 11, Name: "interlinked"},
		13: {PID: 13, PPID: 10, Name: "interlinked"},
		20: {PID: 20, PPID: 1, Name: "codex"},
		21: {PID: 21, PPID: 20, Name: "uvx"},
		22: {PID: 22, PPID: 21, Name: "python"},
		23: {PID: 23, PPID: 22, Name: "interlinked"},
	}
	for self, want := range map[int]int{12: 10, 13: 10, 23: 20} {
		if got, _ := anchorFrom(table, self); got != want {
			t.Errorf("anchor of %d = %d, want %d", self, got, want)
		}
	}
	// Parent gone: fall back to the raw parent pid.
	if got, _ := anchorFrom(map[int]Info{5: {PID: 5, PPID: 99, Name: "interlinked"}}, 5); got != 99 {
		t.Errorf("orphan fallback = %d", got)
	}
}

func TestAnchorOverrideAndLiveTree(t *testing.T) {
	t.Setenv("INTERLINKED_ANCHOR_PID", "4242")
	if pid, _ := Anchor(); pid != 4242 {
		t.Fatalf("override = %d", pid)
	}
	t.Setenv("INTERLINKED_ANCHOR_PID", "")
	os.Unsetenv("INTERLINKED_ANCHOR_PID")
	if pid, _ := Anchor(); pid <= 0 {
		t.Fatal("no anchor found in the real process tree")
	}
	if !Alive(os.Getpid()) || IsWrapper("claude") || !IsWrapper("BASH.EXE") {
		t.Fatal("Alive/IsWrapper")
	}
}
