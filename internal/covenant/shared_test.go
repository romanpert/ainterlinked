package covenant

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedSections(t *testing.T) {
	c := Parse("# x\n## shared: NEXT.md, `docs/adr/`, src/*.log\n- Edit only your lines.\n## Shared: README.md\n## src/**\n- Tests first.\n## shared: *\n- nonsense\n## shared: **/*\n- wide\n")
	for rel, want := range map[string]bool{
		"NEXT.md": true, "next.md": true, "./NEXT.md": true, "README.md": true,
		// Only plain files: a glob or a folder could free the whole repo.
		"docs/adr/decisions.md": false, "src/a.log": false, "src/a.go": false, "x/y/z.md": false,
	} {
		if got := c.IsShared(rel); got != want {
			t.Errorf("IsShared(%q) = %v, want %v", rel, got, want)
		}
	}
	// A shared section's rules reach the agent at the first touch, as any
	// area's, including where it shares nothing.
	if s := c.For("NEXT.md"); len(s) != 2 || s[0].Rules[0] != "Edit only your lines." || s[1].Rules[0] != "wide" {
		t.Fatalf("rules for NEXT.md: %+v", s)
	}
	if s := c.For("docs/adr/x.md"); len(s) != 2 {
		t.Fatalf("rules for docs/adr: %+v", s)
	}
	// "shared: *" is the core section; "shared: **/*" shares nothing and
	// stays an area (its rules are not briefed to everyone).
	if got := c.Core(); len(got) != 1 || got[0] != "nonsense" {
		t.Fatalf("core: %q", got)
	}
	probs := strings.Join(c.Problems(), "\n")
	if strings.Contains(probs, "line 4: section without rules") {
		t.Fatalf("a rule-less shared section was flagged: %s", probs)
	}
	for _, want := range []string{"ignored: docs/adr/**, src/*.log", "ignored: **/*"} {
		if !strings.Contains(probs, want) {
			t.Errorf("problems should say %q:\n%s", want, probs)
		}
	}
	var nilC *Covenant
	if nilC.IsShared("NEXT.md") {
		t.Fatal("no covenant shares nothing")
	}
}

// What frees a file from reservations is the covenant as committed: an
// agent's uncommitted edit to it frees nothing.
func TestSharesReadsTheCommittedCovenant(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	old := Recheck
	Recheck = 0
	defer func() { Recheck = old }()

	root := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, ".ainterlinked"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, ".ainterlinked", "covenant.md"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	write("## shared: NEXT.md\n")
	if Shares(root, "NEXT.md") {
		t.Fatal("an uncommitted covenant shares nothing")
	}
	run("add", ".")
	run("commit", "-q", "-m", "covenant")
	if !Shares(root, "NEXT.md") || Shares(root, "README.md") {
		t.Fatal("the committed covenant was not read")
	}
	write("## shared: NEXT.md, src/held.go\n")
	if Shares(root, "src/held.go") {
		t.Fatal("an uncommitted amendment freed a file")
	}
	run("commit", "-q", "-am", "amend")
	if !Shares(root, "src/held.go") {
		t.Fatal("a committed amendment was not read")
	}
	if Shares(`\\server\share\repo`, "NEXT.md") || Shares("relative/repo", "NEXT.md") {
		t.Fatal("only local absolute roots are looked up")
	}
}

func TestSharesLooksUpAtMostEveryRecheck(t *testing.T) {
	old := Recheck
	Recheck = time.Hour
	defer func() { Recheck = old }()
	root := t.TempDir() // not a repo: nothing shared, and not asked again
	if Shares(root, "NEXT.md") {
		t.Fatal("not a repo")
	}
	e := sharedCache.m[root]
	first := e.checked
	Shares(root, "NEXT.md")
	if !e.checked.Equal(first) {
		t.Fatal("looked up again within Recheck")
	}
}
