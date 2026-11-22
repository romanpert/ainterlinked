package covenant

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `# Covenant of shop

Anything before the first section is for the humans reading this.
<!-- a comment
## not/a/section
-->

## *
- Run the tests before you commit.
- Answer peers only when it adds something.

## db/migrations/, ` + "`schema.sql`" + `
- Never edit a migration that is merged: add a new one.
- Every migration needs a down step,
  tested locally.

## src/**/*.go
Keep functions under 50 lines.

## docs/**
`

func TestParse(t *testing.T) {
	c := Parse(sample)
	if got := c.Core(); len(got) != 2 || got[0] != "Run the tests before you commit." {
		t.Fatalf("core: %q", got)
	}
	if c.Areas() != 2 {
		t.Fatalf("areas = %d, want 2 (an empty section has no rules)", c.Areas())
	}
	mig := c.For("db/migrations/0003_add_total.sql")
	if len(mig) != 1 || len(mig[0].Rules) != 2 || !strings.HasSuffix(mig[0].Rules[1], "down step, tested locally.") {
		t.Fatalf("migrations: %+v", mig)
	}
	if s := c.For("schema.sql"); len(s) != 1 || s[0].Key() != "db/migrations/**,schema.sql" {
		t.Fatalf("schema.sql: %+v", s)
	}
	if s := c.For("./src/cart/total.go"); len(s) != 1 || s[0].Rules[0] != "Keep functions under 50 lines." {
		t.Fatalf("go files: %+v", s)
	}
	if s := c.For("SRC/Cart/Total.go"); len(s) != 1 {
		t.Fatal("matching must not depend on case (Windows paths)")
	}
	for _, f := range []string{"README.md", "docs/a.md", "not/a/section/x"} {
		if s := c.For(f); len(s) != 0 {
			t.Errorf("%s: unexpected rules %+v", f, s)
		}
	}
	if Parse(sample).Hash != c.Hash || Parse(sample+"- one more\n").Hash == c.Hash {
		t.Fatal("hash must follow the content")
	}
	if p := c.Problems(); len(p) != 1 || !strings.Contains(p[0], "without rules") {
		t.Fatalf("problems: %q", p)
	}
}

func TestBulletsStayWithinBudget(t *testing.T) {
	rules := []string{strings.Repeat("a", 50), strings.Repeat("b", 50), strings.Repeat("c", 50)}
	got := Bullets(rules, 120)
	if strings.Contains(got, "ccc") || !strings.Contains(got, "1 more in "+File) {
		t.Fatalf("got %q", got)
	}
	if got := Bullets(rules, 1000); strings.Count(got, "\n- ") != 3 {
		t.Fatalf("got %q", got)
	}
}

func TestOversizedCovenantIsFlagged(t *testing.T) {
	src := "## *\n" + strings.Repeat("- keep it short\n", 80) + "## a/**\n" + strings.Repeat("- x x x x x x x x x\n", 400)
	p := Parse(src).Problems()
	if len(p) != 3 {
		t.Fatalf("want size, core and area problems, got %q", p)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if c, err := Load(dir); c != nil || err != nil {
		t.Fatalf("no file: %v %v", c, err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ainterlinked"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(File)), []byte("## *\r\n- crlf works\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil || len(c.Core()) != 1 || c.Core()[0] != "crlf works" {
		t.Fatalf("%+v %v", c, err)
	}
}
