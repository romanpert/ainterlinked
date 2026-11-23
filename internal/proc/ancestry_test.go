package proc

import (
	"errors"
	"os"
	"runtime"
	"testing"
)

// Measured 2026-11-23: an agent's Git Bash ran `timeout 30 ainterlinked`.
// MSYS exec'd timeout from a forked bash that then exited, so the Windows
// chain stops at timeout, far below claude.exe.
func TestAncestryBrokenByAWrapperIsNotWhole(t *testing.T) {
	table := map[int]Info{
		10820: {PID: 10820, PPID: 10720, Name: "explorer"},
		56352: {PID: 56352, PPID: 10820, Name: "claude"},
		13728: {PID: 13728, PPID: 56352, Name: "bash"},
		54200: {PID: 54200, PPID: 27848, Name: "timeout"}, // 27848 has exited
		52228: {PID: 52228, PPID: 54200, Name: "timeout"},
		19896: {PID: 19896, PPID: 52228, Name: "ainterlinked"},
	}
	chain, err := ancestryIn(table, 19896)
	if err != nil || len(chain) != 2 || chain[1].PID != 54200 {
		t.Fatalf("chain = %+v %v", chain, err)
	}
	for _, goos := range []string{"windows", "linux", "darwin"} {
		if rootedOn(goos, chain) {
			t.Errorf("%s: a chain broken by a wrapper passed as whole: %+v", goos, chain)
		}
	}
}

// Chains measured on this kind of box, each ending at a root.
func TestAncestryOfTheHumansTerminalsIsRooted(t *testing.T) {
	win := map[int]Info{
		// explorer > Code > Code > powershell (VS Code's terminal)
		10820: {PID: 10820, PPID: 10720, Name: "explorer", Created: 100},
		37800: {PID: 37800, PPID: 10820, Name: "code", Created: 200},
		32500: {PID: 32500, PPID: 37800, Name: "code", Created: 300},
		41000: {PID: 41000, PPID: 32500, Name: "powershell", Created: 400},
		41001: {PID: 41001, PPID: 41000, Name: "ainterlinked", Created: 500},
		// wininit > services > svchost > WindowsTerminal > pwsh
		700:   {PID: 700, PPID: 600, Name: "wininit", Created: 10},
		800:   {PID: 800, PPID: 700, Name: "services", Created: 11},
		1200:  {PID: 1200, PPID: 800, Name: "svchost", Created: 12},
		50000: {PID: 50000, PPID: 1200, Name: "windowsterminal", Created: 600},
		50001: {PID: 50001, PPID: 50000, Name: "pwsh", Created: 601},
		50002: {PID: 50002, PPID: 50001, Name: "ainterlinked", Created: 602},
		// winlogon > explorer (restarted) > cmd
		650:   {PID: 650, PPID: 640, Name: "winlogon", Created: 9},
		60000: {PID: 60000, PPID: 650, Name: "explorer", Created: 700},
		60001: {PID: 60001, PPID: 60000, Name: "cmd", Created: 701},
		60002: {PID: 60002, PPID: 60001, Name: "ainterlinked", Created: 702},
	}
	for _, pid := range []int{41001, 50002, 60002} {
		chain, err := ancestryIn(win, pid)
		if err != nil || !rootedOn("windows", chain) {
			t.Errorf("windows %d: %+v %v", pid, chain, err)
		}
	}
	// systemd > gnome-terminal-server > bash; launchd > Terminal > zsh;
	// tmux's server was double-forked, so it hangs under pid 1 too.
	unix := map[int]Info{
		1:    {PID: 1, PPID: 0, Name: "systemd"},
		900:  {PID: 900, PPID: 1, Name: "systemd"},
		1500: {PID: 1500, PPID: 900, Name: "gnome-terminal-server"},
		1501: {PID: 1501, PPID: 1500, Name: "bash"},
		1502: {PID: 1502, PPID: 1501, Name: "ainterlinked"},
		2000: {PID: 2000, PPID: 1, Name: "tmux: server"},
		2001: {PID: 2001, PPID: 2000, Name: "zsh"},
		2002: {PID: 2002, PPID: 2001, Name: "ainterlinked"},
	}
	for _, pid := range []int{1502, 2002} {
		chain, err := ancestryIn(unix, pid)
		if err != nil || !rootedOn("linux", chain) || !rootedOn("darwin", chain) {
			t.Errorf("unix %d: %+v %v", pid, chain, err)
		}
	}
}

// Windows reuses PIDs and never reparents: a parent PID that now names a
// newer process is a parent that has exited, not that process.
func TestAncestryStopsAtAReusedParentPID(t *testing.T) {
	table := map[int]Info{
		// timeout's parent (a forked bash, gone) had PID 27848, which a
		// later explorer-rooted process now holds.
		10820: {PID: 10820, PPID: 10720, Name: "explorer", Created: 100},
		27848: {PID: 27848, PPID: 10820, Name: "notepad", Created: 900},
		54200: {PID: 54200, PPID: 27848, Name: "timeout", Created: 800},
		54201: {PID: 54201, PPID: 54200, Name: "ainterlinked", Created: 801},
	}
	chain, err := ancestryIn(table, 54201)
	if err != nil || len(chain) != 1 || rootedOn("windows", chain) {
		t.Fatalf("a reused parent PID joined the chain: %+v %v", chain, err)
	}
	// explorer's own parent PID reused: explorer is still the root.
	table[10720] = Info{PID: 10720, PPID: 4, Name: "svchost", Created: 950}
	table[54200] = Info{PID: 54200, PPID: 10820, Name: "cmd", Created: 800}
	chain, err = ancestryIn(table, 54201)
	if err != nil || !rootedOn("windows", chain) {
		t.Fatalf("explorer whose parent PID was reused: %+v %v", chain, err)
	}
}

func TestAncestryRefusesWhatItCantTrust(t *testing.T) {
	if _, err := ancestryIn(map[int]Info{}, 5); !errors.Is(err, errNotRunning) {
		t.Errorf("missing pid: %v", err)
	}
	loop := map[int]Info{5: {PID: 5, PPID: 6, Name: "a"}, 6: {PID: 6, PPID: 7, Name: "b"}, 7: {PID: 7, PPID: 6, Name: "c"}}
	if _, err := ancestryIn(loop, 5); !errors.Is(err, errLoop) {
		t.Errorf("loop: %v", err)
	}
	deep := map[int]Info{}
	for i := 1; i <= 100; i++ {
		deep[i] = Info{PID: i, PPID: i + 1, Name: "sh"}
	}
	if _, err := ancestryIn(deep, 1); !errors.Is(err, errLoop) {
		t.Errorf("too deep: %v", err)
	}
}

func TestRootedNeedsTheRightTop(t *testing.T) {
	cases := []struct {
		goos  string
		chain []Info
		want  bool
	}{
		{"windows", nil, false},
		{"linux", nil, false},
		{"windows", []Info{{PID: 1, Name: "bash"}}, false},       // pid 1 means nothing on Windows
		{"windows", []Info{{PID: 9, Name: "services"}}, false},   // only wininit's whole chain
		{"windows", []Info{{PID: 9, Name: "explorer"}}, true},    //
		{"linux", []Info{{PID: 9, Name: "explorer"}}, false},     // names mean nothing on Unix
		{"linux", []Info{{PID: 1, Name: "init"}}, true},          //
		{"darwin", []Info{{PID: 1, Name: "launchd"}}, true},      //
		{"linux", []Info{{PID: 1}, {PID: 77, Name: "x"}}, false}, // the top counts, not the middle
	}
	for _, c := range cases {
		if got := rootedOn(c.goos, c.chain); got != c.want {
			t.Errorf("rootedOn(%s, %+v) = %v, want %v", c.goos, c.chain, got, c.want)
		}
	}
}

// The real table: this test process's chain is whole when run from a
// terminal or an IDE; under an MSYS wrapper it can be broken, so only the
// walk itself is checked, plus creation order where the OS reports it.
func TestAncestryOfThisProcess(t *testing.T) {
	chain, err := Ancestry(os.Getpid())
	if err != nil {
		t.Skip("no process table here:", err)
	}
	if len(chain) == 0 {
		t.Fatal("no ancestors at all")
	}
	if runtime.GOOS == "windows" {
		prev := int64(1 << 62)
		for _, p := range chain {
			if p.Created == 0 || p.Created > prev {
				t.Fatalf("creation times out of order: %+v", chain)
			}
			prev = p.Created
		}
	}
	t.Logf("chain: %+v rooted=%v", chain, Rooted(chain))
}
