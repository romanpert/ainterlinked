package mcp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// A reservation needn't name a file: any name the user agrees on is a turn
// (a heavy build, a GPU, a test database), with its queue. The README says so.
func TestAnyNameIsATurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.NewLocal(st)
	project := filepath.Join(t.TempDir(), "shop")
	claude := start(t, h, "claude-code", "11", project)
	codex := start(t, h, "codex", "22", project)
	take := func(c *client, name string) string {
		t.Helper()
		out, isErr := c.tool("reserve_files", map[string]any{"paths": []string{name}, "wait": true})
		if isErr {
			t.Fatal(out)
		}
		return out
	}
	if out := take(claude, "turn:build"); !strings.Contains(out, "reserved:") {
		t.Fatal(out)
	}
	if out := take(codex, "turn:build"); !strings.Contains(out, "#1 in line, held by claude@shop") {
		t.Fatal(out)
	}
	if out := take(codex, "turn:gpu"); !strings.Contains(out, "reserved:") {
		t.Fatalf("another name is another turn: %s", out)
	}
	if out, isErr := claude.tool("release_files", map[string]any{"paths": []string{"turn:build"}}); isErr {
		t.Fatal(out)
	}
	if out, _ := codex.tool("read_inbox", nil); !strings.Contains(out, "reserved for you: turn:build") {
		t.Fatalf("the next in line is told: %s", out)
	}
}
