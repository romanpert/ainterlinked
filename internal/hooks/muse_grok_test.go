package hooks

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

func TestMuseAndGrokAreRefusedAReservedFile(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: e.agent("1").ID, Paths: []string{"src/"}, Reason: "task #4"}, &res); err != nil || len(res.Granted) != 1 {
		t.Fatal(err, res)
	}
	target := filepath.Join(e.dir, "src", "main.go")
	for pid, c := range map[string]struct {
		harness, tool string
		camel         bool
	}{"2": {"muse", "edit_file", false}, "3": {"grok", "search_replace", true}} {
		payload := map[string]any{"tool_name": c.tool, "tool_input": map[string]any{"file_path": target}}
		if c.camel {
			payload = map[string]any{"toolName": c.tool, "toolInput": map[string]any{"file_path": target}}
		}
		_, out := e.as(pid, c.harness, "PreToolUse", payload)
		h, _ := out["hookSpecificOutput"].(map[string]any)
		if h["permissionDecision"] != "deny" || h["hookEventName"] != "PreToolUse" {
			t.Errorf("%s: want a deny, got %v", c.harness, out)
		}
		// Muse rejects the whole answer over one unknown key.
		for k := range out {
			if k != "hookSpecificOutput" {
				t.Errorf("%s: unexpected key %q", c.harness, k)
			}
		}
	}
}

// Grok discards SessionStart and UserPromptSubmit output, so mail must not
// be marked read there.
func TestGrokMailWaitsForAnEventThatShowsIt(t *testing.T) {
	e := setup(t)
	e.as("1", "grok", "SessionStart", nil)
	e.as("2", "claude-code", "SessionStart", nil)
	e.send("2", "1", "keep me")
	for _, ev := range []string{"SessionStart", "UserPromptSubmit"} {
		if _, out := e.as("1", "grok", ev, map[string]any{"prompt": "hi"}); out != nil {
			t.Fatalf("%s: unexpected output %v", ev, out)
		}
	}
	var u hub.UnreadResult
	_ = e.c.Call(context.Background(), "unread", hub.AgentParam{Agent: e.agent("1").ID}, &u)
	if u.Total != 1 {
		t.Fatalf("consumed by an event that can't show it (unread=%d)", u.Total)
	}
	_, out := e.as("1", "grok", "PostToolUse", map[string]any{"toolName": "read_file"})
	h, _ := out["hookSpecificOutput"].(map[string]any)
	if s, _ := h["additionalContext"].(string); !strings.Contains(s, "keep me") {
		t.Fatalf("PostToolUse should carry it: %v", out)
	}
}

// Grok Build also runs the hooks in Claude Code's settings: there it must
// not register as a Claude session.
func TestClaudeHooksRunByGrokStaySilent(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	before := e.agent("1")
	t.Setenv("GROK_HOOK_EVENT", "session_start")
	var out bytes.Buffer
	Run(context.Background(), e.cfg, "claude-code", "SessionStart", strings.NewReader("{}"), &out)
	if out.Len() != 0 {
		t.Fatalf("output: %s", out.String())
	}
	if !borrowed("cursor") || borrowed("grok") {
		t.Fatal("only Grok's own hooks run under Grok")
	}
	if after := e.agent("1"); after.ID != before.ID {
		t.Fatal("a second agent was registered")
	}
}
