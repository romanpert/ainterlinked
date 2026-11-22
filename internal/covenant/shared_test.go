package covenant

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSharedSections(t *testing.T) {
	c := Parse("# x\n## shared: NEXT.md, `docs/adr/`\n- Edit only your lines.\n## Shared: README.md\n## src/**\n- Tests first.\n## shared: *\n- nonsense\n")
	for rel, want := range map[string]bool{
		"NEXT.md": true, "next.md": true, "./NEXT.md": true, "docs/adr/decisions.md": true,
		"README.md": true, "src/a.go": false, "docs/other.md": false,
	} {
		if got := c.IsShared(rel); got != want {
			t.Errorf("IsShared(%q) = %v, want %v", rel, got, want)
		}
	}
	// A shared section's rules reach the agent at the first touch, as any area's.
	if s := c.For("NEXT.md"); len(s) != 1 || s[0].Rules[0] != "Edit only your lines." {
		t.Fatalf("rules for NEXT.md: %+v", s)
	}
	// "shared: *" can't make the whole repo nobody's: it is the core section.
	if got := c.Core(); len(got) != 1 || got[0] != "nonsense" {
		t.Fatalf("core: %q", got)
	}
	// A shared section without rules is fine; an ordinary one is flagged.
	for _, p := range c.Problems() {
		if p == "line 4: section without rules" {
			t.Fatalf("a rule-less shared section was flagged: %q", c.Problems())
		}
	}
	var nilC *Covenant
	if nilC.IsShared("NEXT.md") {
		t.Fatal("no covenant shares nothing")
	}
}

func TestSharesFollowsTheFileOnDisk(t *testing.T) {
	root := t.TempDir()
	if Shares(root, "NEXT.md") {
		t.Fatal("no covenant yet")
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
	write("## shared: NEXT.md\n")
	time.Sleep(1100 * time.Millisecond) // past the once-a-second check
	if !Shares(root, "NEXT.md") || Shares(root, "README.md") {
		t.Fatal("the new covenant was not read")
	}
	write("## shared: NEXT.md, README.md\n")
	time.Sleep(1100 * time.Millisecond)
	if !Shares(root, "README.md") {
		t.Fatal("an amended covenant was not read again")
	}
}
