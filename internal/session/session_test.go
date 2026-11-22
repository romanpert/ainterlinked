package session

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/romanpert/ainterlinked/internal/config"
)

// A checkout reached through a symlink (macOS /var -> /private/var) must get
// the same project path as the real directory, and so must files in it,
// including ones that don't exist yet.
func TestCanonicalFollowsSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Canonical leaves Windows paths alone")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("no symlinks here:", err)
	}

	if got := Canonical(filepath.Join(link, "db", "new.sql")); got != filepath.Join(real, "db", "new.sql") {
		t.Fatalf("Canonical(missing file via link) = %s", got)
	}
	if got := Canonical("relative/path"); got != "relative/path" {
		t.Fatalf("relative path changed: %s", got)
	}
	viaLink := Detect(config.Config{}, "codex", link)
	direct := Detect(config.Config{}, "codex", real)
	if viaLink.ProjectPath != direct.ProjectPath || viaLink.ProjectPath != NormPath(real) {
		t.Fatalf("project via link %q, direct %q", viaLink.ProjectPath, direct.ProjectPath)
	}
}
