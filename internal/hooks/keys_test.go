package hooks

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// Each hook is its own process: a fresh client that only knows the key its
// own registration gave it. It still resolves its session's agent and acts.
func TestEachHookProcessActsAsItsOwnAgent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	run := func(event string, payload map[string]any) {
		t.Helper()
		t.Setenv("AINTERLINKED_ANCHOR_PID", "300")
		payload["cwd"] = e.dir
		raw, _ := json.Marshal(payload)
		if _, err := Handle(ctx, e.cfg, hub.NewLocal(e.c.Store), "claude-code", event, ParseInput(raw)); err != nil {
			t.Fatalf("%s: %v", event, err)
		}
	}
	run("SessionStart", map[string]any{"session_id": "k1"})
	target := filepath.Join(e.dir, "src", "app.go")
	run("PreToolUse", map[string]any{"session_id": "k1", "tool_name": "Edit", "tool_input": map[string]any{"file_path": target}})
	me := e.agent("300")
	var locks []core.Lock
	if err := e.c.Call(ctx, "locks", hub.ProjectParam{}, &locks); err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].AgentID != me.ID {
		t.Fatalf("the edit must reserve the file for the hook's own agent (touch_file with its key): %+v", locks)
	}
	run("Stop", map[string]any{"session_id": "k1"})
	if a := e.agent("300"); a.State != "idle" {
		t.Fatalf("stop hook's status must apply: %+v", a)
	}
}
