package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/romanpert/ainterlinked/internal/session"
)

// A transcript is handed over only to the checkout it ran in, resolved the
// way registration resolves it: a project path claimed as a parent folder,
// a sibling repo or a subfolder gets nothing.
func TestAHandoverStaysInTheExactCheckoutItRanIn(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "shop")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, "web"), filepath.Join(base, "bank", ".git"), filepath.Join(base, "loose")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	const sid = "00000000-test-4000-8000-handoverhub1"
	ran := func(cwd string) {
		config := filepath.Join(t.TempDir(), ".claude")
		t.Setenv("CLAUDE_CONFIG_DIR", config)
		dir := filepath.Join(config, "projects", "c--proj")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		lines := fmt.Sprintf(`{"type":"user","isMeta":true,"cwd":%q,"message":{"role":"user","content":"x"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"fix it"}]},"origin":{"kind":"human"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"fixed in auth.go"}]}}`+"\n", cwd)
		if err := os.WriteFile(filepath.Join(dir, sid+".jsonl"), []byte(lines), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	project := func(dir string) string { return session.NormPath(session.Canonical(dir)) }

	ran(filepath.Join(repo, "web"))
	if _, ok := handoverIn("claude-code", sid, project(repo)); !ok {
		t.Fatal("its own checkout got nothing")
	}
	for _, other := range []string{project(base), project(filepath.Dir(base)), project(filepath.Join(repo, "web")), project(filepath.Join(base, "bank")), ""} {
		if _, ok := handoverIn("claude-code", sid, other); ok {
			t.Fatalf("handed shop's transcript to %q", other)
		}
	}

	ran(filepath.Join(base, "loose")) // outside a repo, the folder itself
	if _, ok := handoverIn("claude-code", sid, project(filepath.Join(base, "loose"))); !ok {
		t.Fatal("a folder outside a repo got nothing")
	}
	if _, ok := handoverIn("claude-code", sid, project(base)); ok {
		t.Fatal("a parent folder took a loose folder's transcript")
	}

	ran("web") // a relative path says nothing about where it ran
	if _, ok := handoverIn("claude-code", sid, project(repo)); ok {
		t.Fatal("a relative working directory was trusted")
	}
}
