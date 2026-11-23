package mcp

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// list_tasks shows open tasks and one line for the closed ones; status
// lists those, and query searches every task.
func TestListTasksHidesClosedAndSearches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	h := hub.NewLocal(st)
	c := start(t, h, "claude-code", "11", filepath.Join(t.TempDir(), "shop"))
	for _, title := range []string{"Reservas de sala", "Ship it", "Drop it"} {
		if out, isErr := c.tool("create_task", map[string]any{"title": title, "body": "notes about " + title}); isErr {
			t.Fatal(out)
		}
	}
	_, _ = c.tool("update_task", map[string]any{"id": 2, "status": "done"})
	_, _ = c.tool("update_task", map[string]any{"id": 3, "status": "cancelled"})

	out, _ := c.tool("list_tasks", nil)
	if !strings.Contains(out, "#1 [todo]") || strings.Contains(out, "Ship it") ||
		!strings.HasSuffix(out, "\n2 closed (status=done or status=cancelled to see them)") {
		t.Fatalf("default board: %s", out)
	}
	if out, _ = c.tool("list_tasks", map[string]any{"status": "done"}); !strings.Contains(out, "#2 [done]") || strings.Contains(out, "closed (") {
		t.Fatalf("status=done: %s", out)
	}
	out, _ = c.tool("list_tasks", map[string]any{"query": "about reservs"})
	if !strings.HasPrefix(out, "#1 [todo]") || !strings.Contains(out, "match: notes about Reservas de sala") {
		t.Fatalf("search: %s", out)
	}
	if out, _ = c.tool("list_tasks", map[string]any{"query": "it", "limit": 1}); !strings.Contains(out, "1 more match") {
		t.Fatalf("limit: %s", out)
	}
	if out, _ = c.tool("list_tasks", map[string]any{"query": "nowhere"}); out != `No task matches "nowhere".` {
		t.Fatalf("no match: %s", out)
	}
}
