package mcp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// A holder hands a file to the agent it chooses among those waiting, and
// list_agents shows what each agent has done lately, by fact.
func TestCessionAndFactsOverMCP(t *testing.T) {
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
	gemini := start(t, h, "gemini", "33", project)
	for _, c := range []*client{claude, codex, gemini} {
		_, _ = c.tool("whoami", nil) // agents join on their first call
	}
	if out, isErr := claude.tool("reserve_files", map[string]any{"paths": []string{"cart.js"}}); isErr {
		t.Fatal(out)
	}
	if out, isErr := claude.tool("release_files", map[string]any{"paths": []string{"cart.js"}, "to": "codex@shop"}); !isErr || !strings.Contains(out, "isn't queued") {
		t.Fatalf("cession needs the receiver's queue place: %s", out)
	}
	_, _ = gemini.tool("reserve_files", map[string]any{"paths": []string{"cart.js"}, "wait": true})
	_, _ = codex.tool("reserve_files", map[string]any{"paths": []string{"cart.js"}, "wait": true})
	if out, isErr := claude.tool("release_files", map[string]any{"paths": []string{"cart.js"}, "to": "codex@shop"}); isErr || out != "ceded 1 reservation(s) to codex@shop" {
		t.Fatalf("cede: %s", out)
	}
	_, _ = claude.tool("send_message", map[string]any{"to": "gemini@shop", "body": "are you on it?"})
	list, _ := claude.tool("list_agents", nil)
	if !strings.Contains(list, "facts, not a score") || !strings.Contains(list, "codex@shop: holds cart.js") ||
		!strings.Contains(list, "gemini@shop: 1 of your messages unread") {
		t.Fatalf("list_agents facts: %s", list)
	}
}
