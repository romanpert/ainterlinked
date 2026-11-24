package mcp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// release_files lets go of what it names and nothing else: paths sent as
// one string (models do) is that one path, and an empty list is a mistake,
// not "all of them". Only leaving paths out releases everything.
func TestReleaseFilesReleasesOnlyWhatItNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	claude := start(t, hub.NewLocal(st), "claude-code", "11", filepath.Join(t.TempDir(), "shop"))
	if out, isErr := claude.tool("reserve_files", map[string]any{"paths": []string{"cart.js", "menu.js", "pay.js"}}); isErr {
		t.Fatal(out)
	}
	held := func() string { out, _ := claude.tool("list_reservations", nil); return out }

	if out, isErr := claude.tool("release_files", map[string]any{"paths": "cart.js"}); isErr || out != "released 1 reservation(s)" {
		t.Fatalf("paths as one string: %s", out)
	}
	if h := held(); strings.Contains(h, "cart.js") || !strings.Contains(h, "menu.js") || !strings.Contains(h, "pay.js") {
		t.Fatalf("after releasing cart.js: %s", h)
	}
	for _, empty := range []any{[]string{}, "", []string{""}, 7} {
		if out, isErr := claude.tool("release_files", map[string]any{"paths": empty}); !isErr || !strings.Contains(out, "paths") {
			t.Fatalf("paths %#v released: %s", empty, out)
		}
	}
	if h := held(); !strings.Contains(h, "menu.js") || !strings.Contains(h, "pay.js") {
		t.Fatalf("a bad paths released everything: %s", h)
	}
	if out, isErr := claude.tool("release_files", nil); isErr || out != "released 2 reservation(s)" {
		t.Fatalf("no paths releases all: %s", out)
	}
}
