package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// Past the owner's cap every tool call is refused, with the figures and who
// can raise it, and a turn isn't held open for peer mail any more.
func TestPastTheOwnersCapNothingMoreRuns(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.as("1", "claude-code", "SessionStart", map[string]any{"session_id": "s1"})
	me := e.agent("1")
	e.as("2", "codex", "SessionStart", map[string]any{"session_id": "c2"})
	peer := e.agent("2")

	edit := map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}}
	if res, _ := e.as("1", "claude-code", "PreToolUse", edit); res.Deny != "" {
		t.Fatalf("no cap yet, refused: %s", res.Deny)
	}
	st := e.c.Store
	if err := st.RecordSpend(ctx, me.ID, transcript.Ledger{transcript.CauseOwn: {In: 1000, CacheWrite: 100_000, CacheRead: 9_000_000, Out: 50_000}}); err != nil {
		t.Fatal(err)
	}
	owner, err := st.Register(ctx, core.RegisterReq{Harness: "human", Name: "human", Machine: "box", SessionID: "human@box"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetBudget(ctx, core.BudgetReq{By: owner.ID, SessionTokens: 200_000}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.as("1", "claude-code", "PreToolUse", edit); res.Deny != "" {
		t.Fatalf("under the cap (151k of 200k; cache reads aside), refused: %s", res.Deny)
	}
	if _, err := st.SetBudget(ctx, core.BudgetReq{By: owner.ID, SessionTokens: 150_000}); err != nil {
		t.Fatal(err)
	}
	res, out := e.as("1", "claude-code", "PreToolUse", edit)
	if !strings.Contains(res.Deny, "151k billed tokens") || !strings.Contains(res.Deny, "cap for one session is 150k") || !strings.Contains(res.Deny, "ainterlinked budget") {
		t.Fatalf("over the cap: %q", res.Deny)
	}
	if hso, _ := out["hookSpecificOutput"].(map[string]any); hso["permissionDecision"] != "deny" {
		t.Fatalf("not denied to the harness: %v", out)
	}
	// Peer mail doesn't keep a turn going past the cap.
	if _, err := st.Send(ctx, core.SendReq{From: peer.ID, To: me.Name, Body: "one more thing"}); err != nil {
		t.Fatal(err)
	}
	if res, _ := e.as("1", "claude-code", "Stop", map[string]any{"session_id": "s1"}); res.Block != "" {
		t.Fatalf("held open for a peer past the cap: %s", res.Block)
	}
}
