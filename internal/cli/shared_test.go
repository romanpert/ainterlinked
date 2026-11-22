package cli

import (
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/render"
)

// What the owner reads before accepting must be what gets accepted: a name
// from the repo can't hide itself or rewrite the line with terminal codes.
func TestSharedListIsPrintedAsIs(t *testing.T) {
	for in, want := range map[string]string{
		"NEXT.md":             "NEXT.md",
		"docs/año.md":         "docs/año.md",
		"x\x1b[2K\rREADME.md": `"x\x1b[2K\rREADME.md"`,
		"evil‮gm.md":          "\"evil\\u202egm.md\"",
		"a\nb":                `"a\nb"`,
	} {
		if got := printable(in); got != want {
			t.Errorf("printable(%q) = %s, want %s", in, got, want)
		}
	}
	if !sameFiles([]string{"next.md", "docs/log.md"}, []string{"./docs/log.md", "NEXT.md"}) || sameFiles([]string{"a"}, []string{"b"}) {
		t.Fatal("sameFiles")
	}
}

// A refusal is not a lease: nothing about it expires.
func TestARefusalHasNoExpiry(t *testing.T) {
	out := render.Locks([]core.Lock{{Pattern: "next.md", Agent: core.SystemSender, Reason: "shared by the owner"}})
	if strings.Contains(out, "expires") {
		t.Fatalf("refusal rendered with an expiry: %s", out)
	}
}
