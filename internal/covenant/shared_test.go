package covenant

import (
	"strings"
	"testing"
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
