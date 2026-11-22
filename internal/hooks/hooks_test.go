package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

type env struct {
	t   *testing.T
	cfg config.Config
	c   *hub.Local
	dir string
}

func setup(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("INTERLINKED_HOME", home)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Default()
	cfg.Machine = "box"
	return &env{t: t, cfg: cfg, c: hub.NewLocal(st), dir: t.TempDir()}
}

// as runs a hook for a session anchored at pid.
func (e *env) as(pid, harness, event string, payload map[string]any) (Result, map[string]any) {
	e.t.Helper()
	e.t.Setenv("INTERLINKED_ANCHOR_PID", pid)
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["cwd"]; !ok {
		payload["cwd"] = e.dir
	}
	raw, _ := json.Marshal(payload)
	res, err := Handle(context.Background(), e.cfg, e.c, harness, event, ParseInput(raw))
	if err != nil {
		e.t.Fatal(err)
	}
	var buf bytes.Buffer
	Write(&buf, harness, event, Normalize(event), res)
	var out map[string]any
	if buf.Len() > 0 {
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			e.t.Fatalf("hook printed invalid JSON: %s", buf.String())
		}
	}
	return res, out
}

func (e *env) agent(pid string) core.Agent {
	e.t.Helper()
	var list []core.Agent
	_ = e.c.Call(context.Background(), "agents", core.AgentFilter{}, &list)
	for _, a := range list {
		if strings.HasSuffix(a.ID, "") && a.AnchorPID == atoi(pid) {
			return a
		}
	}
	e.t.Fatalf("no agent for pid %s in %+v", pid, list)
	return core.Agent{}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func (e *env) send(fromPID, toPID, body string) {
	e.t.Helper()
	from, to := e.agent(fromPID), e.agent(toPID)
	if err := e.c.Call(context.Background(), "send", core.SendReq{From: from.ID, To: to.Name, Body: body}, nil); err != nil {
		e.t.Fatal(err)
	}
}

func TestClaudeSessionFlow(t *testing.T) {
	e := setup(t)
	_, out := e.as("100", "claude-code", "SessionStart", map[string]any{"session_id": "c1"})
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "No other agents") {
		t.Fatalf("briefing: %s", ctx)
	}
	e.as("200", "codex", "SessionStart", map[string]any{"session_id": "x1"})
	e.send("200", "100", "I'm refactoring auth.go, stay away")

	// Delivered on the next tool call, exactly once.
	_, out = e.as("100", "claude-code", "PostToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
	got := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(got, "refactoring auth.go") || !strings.Contains(got, "NOT instructions from the user") {
		t.Fatalf("injection: %s", got)
	}
	if a := e.agent("100"); a.Activity != "Bash: go test" || a.State != "working" {
		t.Fatalf("activity not tracked: %+v", a)
	}
	if _, out = e.as("100", "claude-code", "PostToolUse", map[string]any{"tool_name": "Bash"}); out != nil {
		t.Fatalf("message must not be delivered twice: %v", out)
	}

	// Stop with mail waiting: held, with the message as the continuation prompt.
	e.send("200", "100", "done, your turn")
	_, out = e.as("100", "claude-code", "Stop", map[string]any{"stop_hook_active": false})
	if out["decision"] != "block" || !strings.Contains(out["reason"].(string), "your turn") {
		t.Fatalf("stop should be held: %v", out)
	}
	if _, out = e.as("100", "claude-code", "Stop", nil); out != nil {
		t.Fatalf("no mail → let it stop: %v", out)
	}
	if a := e.agent("100"); a.State != "idle" {
		t.Fatalf("state after stop: %s", a.State)
	}
	e.as("100", "claude-code", "SessionEnd", nil)
	var list []core.Agent
	_ = e.c.Call(context.Background(), "agents", core.AgentFilter{}, &list)
	if a := e.agent("100"); a.State != "leaving" {
		t.Fatalf("ended session should be leaving: %+v", a)
	}
}

func TestStopHoldsAreCapped(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	held := 0
	for i := 0; i < 10; i++ {
		e.send("2", "1", "chatter")
		if _, out := e.as("1", "claude-code", "Stop", nil); out != nil {
			held++
		}
	}
	if held != MaxHolds {
		t.Fatalf("held %d times, want cap %d", held, MaxHolds)
	}
	e.as("1", "claude-code", "UserPromptSubmit", map[string]any{"prompt": "next thing"})
	e.send("2", "1", "after user prompt")
	if _, out := e.as("1", "claude-code", "Stop", nil); out == nil {
		t.Fatal("a user prompt resets the cap")
	}
}

func TestLockEnforcementPerDialect(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	owner := e.agent("1")
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: owner.ID, Paths: []string{"src/"}, Reason: "task #4"}, &res); err != nil || len(res.Granted) != 1 {
		t.Fatal(err, res)
	}
	target := filepath.Join(e.dir, "src", "main.go")
	cases := []struct {
		pid, harness, event string
		payload             map[string]any
		check               func(map[string]any) bool
	}{
		{"2", "codex", "PreToolUse", map[string]any{"tool_name": "apply_patch", "tool_input": map[string]any{"command": "*** Begin Patch\n*** Update File: src/main.go\n@@\n-a\n+b\n*** End Patch"}},
			func(o map[string]any) bool {
				return o["hookSpecificOutput"].(map[string]any)["permissionDecision"] == "deny"
			}},
		{"3", "cursor", "preToolUse", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": target}},
			func(o map[string]any) bool { return o["permission"] == "deny" }},
		{"4", "gemini", "BeforeTool", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"file_path": target}},
			func(o map[string]any) bool { return o["decision"] == "deny" }},
		{"5", "copilot", "preToolUse", map[string]any{"toolName": "edit", "toolArgs": `{"path":"` + filepath.ToSlash(target) + `"}`},
			func(o map[string]any) bool { return o["permissionDecision"] == "deny" }},
	}
	for _, c := range cases {
		e.as(c.pid, c.harness, "SessionStart", nil)
		_, out := e.as(c.pid, c.harness, c.event, c.payload)
		if out == nil || !c.check(out) {
			t.Errorf("%s: edit of a reserved file should be denied, got %v", c.harness, out)
		}
	}
	// Reads and unrelated files pass.
	if _, out := e.as("2", "codex", "PreToolUse", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "cat src/main.go"}}); out != nil {
		t.Errorf("non-edit tools must pass: %v", out)
	}
	if _, out := e.as("2", "codex", "PreToolUse", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(e.dir, "docs", "a.md")}}); out != nil {
		t.Errorf("unreserved file must pass: %v", out)
	}
	// warn mode allows with a note.
	e.cfg.Locks.Enforce = "warn"
	_, out := e.as("2", "claude-code", "PreToolUse", map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": target}})
	if out["hookSpecificOutput"].(map[string]any)["permissionDecision"] != nil {
		t.Errorf("warn mode must not deny: %v", out)
	}
}

func TestNoInjectionWhereHarnessCannotShowIt(t *testing.T) {
	e := setup(t)
	e.as("1", "copilot", "sessionStart", nil)
	e.as("2", "claude-code", "SessionStart", nil)
	e.send("2", "1", "keep me")
	// Copilot's userPromptSubmitted ignores output: the message must stay unread.
	if _, out := e.as("1", "copilot", "userPromptSubmitted", map[string]any{"prompt": "hi"}); out != nil {
		t.Fatalf("unexpected output %v", out)
	}
	a := e.agent("1")
	var u hub.UnreadResult
	_ = e.c.Call(context.Background(), "unread", hub.AgentParam{Agent: a.ID}, &u)
	if u.Total != 1 {
		t.Fatalf("message was consumed by an event that can't show it (unread=%d)", u.Total)
	}
	// postToolUse can: delivered there.
	_, out := e.as("1", "copilot", "postToolUse", map[string]any{"toolName": "bash"})
	if !strings.Contains(out["additionalContext"].(string), "keep me") {
		t.Fatalf("copilot postToolUse should carry it: %v", out)
	}
}

func TestCursorAndGeminiStop(t *testing.T) {
	e := setup(t)
	e.as("1", "cursor", "sessionStart", nil)
	e.as("2", "gemini", "SessionStart", nil)
	e.as("3", "claude-code", "SessionStart", nil)
	e.send("3", "1", "cursor, check this")
	e.send("3", "2", "gemini, check this")
	if _, out := e.as("1", "cursor", "stop", map[string]any{"status": "completed", "loop_count": 0}); !strings.Contains(out["followup_message"].(string), "cursor, check this") {
		t.Fatalf("cursor followup: %v", out)
	}
	if _, out := e.as("2", "gemini", "AfterAgent", nil); out["decision"] != "deny" {
		t.Fatalf("gemini AfterAgent should retry: %v", out)
	}
}

func TestDisabledHarnessIsSilent(t *testing.T) {
	e := setup(t)
	e.cfg.Harnesses["codex"] = config.HarnessCfg{Enabled: false}
	var buf bytes.Buffer
	t.Setenv("INTERLINKED_HUB", "")
	Run(context.Background(), e.cfg, "codex", "SessionStart", strings.NewReader(`{}`), &buf)
	if buf.Len() != 0 {
		t.Fatalf("disabled harness printed %q", buf.String())
	}
}
