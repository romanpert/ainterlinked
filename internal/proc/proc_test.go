package proc

import (
	"os"
	"os/exec"
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

func TestAliveSetSeesItself(t *testing.T) {
	self := os.Getpid()
	got, err := AliveSet([]int{self, 0, -1, 1 << 30})
	if err != nil {
		t.Skip("no process table here:", err)
	}
	if !got[self] || got[0] || got[-1] || got[1<<30] {
		t.Fatalf("AliveSet = %v (self %d)", got, self)
	}
}

// The per-process walk (fast path) must agree with the full snapshot.
func TestLookupWalkMatchesSnapshot(t *testing.T) {
	table, err := snapshot()
	if err != nil {
		t.Skip("no process table here:", err)
	}
	wantPID, wantName := anchorFrom(table, os.Getpid())
	gotPID, gotName, ok := anchorWalk(os.Getpid())
	if !ok {
		t.Fatal("per-process walk failed on our own process tree")
	}
	if gotPID != wantPID || gotName != wantName {
		t.Fatalf("walk = %d %q, snapshot = %d %q", gotPID, gotName, wantPID, wantName)
	}
	self, ok := lookup(os.Getpid())
	if !ok || self.PID != os.Getpid() || self.Name == "" || self.PPID <= 0 {
		t.Fatalf("lookup(self) = %+v %v", self, ok)
	}
}

func TestAliveSetSpotsExitedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Skip(err)
	}
	got, err := AliveSet([]int{os.Getpid(), cmd.Process.Pid})
	if err != nil {
		t.Fatal(err)
	}
	if !got[os.Getpid()] || got[cmd.Process.Pid] {
		t.Fatalf("AliveSet = %v", got)
	}
}
