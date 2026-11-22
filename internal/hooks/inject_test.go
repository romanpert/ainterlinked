package hooks

import (
	"context"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
)

func ctxOf(t *testing.T, out map[string]any) string {
	t.Helper()
	if out == nil {
		return ""
	}
	h, ok := out["hookSpecificOutput"].(map[string]any)
	if !ok {
		return ""
	}
	s, _ := h["additionalContext"].(string)
	return s
}

func (e *env) broadcast(fromPID, body string) {
	e.t.Helper()
	if err := e.c.Call(context.Background(), "send", core.SendReq{From: e.agent(fromPID).ID, To: "project", Body: body}, nil); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) prefs(pid string, r core.PrefsReq) {
	e.t.Helper()
	r.Agent = e.agent(pid).ID
	if err := e.c.Call(context.Background(), "set_prefs", r, nil); err != nil {
		e.t.Fatal(err)
	}
}

func tool(e *env, pid string) string {
	_, out := e.as(pid, "claude-code", "PostToolUse", map[string]any{"tool_name": "Read"})
	return ctxOf(e.t, out)
}

// Group announcements cost one line each; a direct message arrives whole.
func TestBroadcastsArriveAsBriefsDirectMailInFull(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	long := "reload of core now: " + strings.Repeat("long detail ", 100)
	e.broadcast("2", long)
	e.send("2", "1", "please review auth.go")
	got := tool(e, "1")
	if !strings.Contains(got, "please review auth.go") || !strings.Contains(got, "reload of core now") {
		t.Fatalf("both should arrive: %s", got)
	}
	if strings.Count(got, "long detail") > 20 {
		t.Fatalf("the broadcast should be a short brief, got %d repeats", strings.Count(got, "long detail"))
	}
	if !strings.Contains(got, "peer-briefs") || strings.Index(got, "please review") > strings.Index(got, "peer-briefs") {
		t.Fatalf("full mail first, briefs after in their own fence: %s", got)
	}
}

// The turn is only held open for mail meant for this agent in full.
func TestStopHoldIgnoresBriefs(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	e.broadcast("2", "FYI: pushed dev")
	if _, out := e.as("1", "claude-code", "Stop", nil); out != nil {
		t.Fatalf("a brief must not hold the turn: %v", out)
	}
	if got := tool(e, "1"); !strings.Contains(got, "pushed dev") {
		t.Fatalf("the brief still arrives at the next tool call: %s", got)
	}
}

// A held turn is the recipient's to give: group mail never holds it, even
// urgent, delivered in full or on a full-mode topic. Mail to its name does,
// and the group mail left waiting arrives at the next tool call.
func TestOnlyDirectMailHoldsTheTurn(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	e.prefs("1", core.PrefsReq{Broadcasts: core.ModeFull, Subscribe: []string{"reload"}})
	for _, to := range []string{"project", "#reload", "*"} {
		if err := e.c.Call(context.Background(), "send", core.SendReq{From: e.agent("2").ID, To: to, Body: "group news via " + to, Urgent: true}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, out := e.as("1", "claude-code", "Stop", nil); out != nil {
		t.Fatalf("group mail must not hold the turn: %v", out)
	}
	e.send("2", "1", "only you: rebase first")
	_, out := e.as("1", "claude-code", "Stop", nil)
	reason, _ := out["reason"].(string)
	if out["decision"] != "block" || !strings.Contains(reason, "rebase first") || strings.Contains(reason, "group news") {
		t.Fatalf("direct mail holds the turn, alone: %v", out)
	}
	if got := tool(e, "1"); strings.Count(got, "group news") != 3 {
		t.Fatalf("the group mail arrives at the next tool call: %s", got)
	}
}

// Agents set their own cadence and limits; what doesn't fit stays unread.
func TestAgentsTuneTheirOwnDelivery(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)

	e.prefs("1", core.PrefsReq{Inject: core.InjectTurn})
	e.send("2", "1", "first")
	if got := tool(e, "1"); got != "" {
		t.Fatalf("inject=turn: nothing after tool calls: %s", got)
	}
	_, out := e.as("1", "claude-code", "UserPromptSubmit", map[string]any{"prompt": "go on"})
	if !strings.Contains(ctxOf(t, out), "first") {
		t.Fatalf("inject=turn delivers at the prompt: %v", out)
	}

	e.prefs("1", core.PrefsReq{Inject: core.InjectManual})
	e.send("2", "1", "second")
	if _, out := e.as("1", "claude-code", "Stop", nil); out != nil {
		t.Fatalf("manual: never held: %v", out)
	}
	_, out = e.as("1", "claude-code", "UserPromptSubmit", map[string]any{"prompt": "x"})
	if ctxOf(t, out) != "" {
		t.Fatalf("manual: nothing injected: %v", out)
	}

	e.prefs("1", core.PrefsReq{Inject: core.InjectEveryTool, MaxMessages: 1, MaxChars: 1500})
	e.send("2", "1", "third")
	got := tool(e, "1")
	if !strings.Contains(got, "second") || strings.Contains(got, "third") || !strings.Contains(got, "+1 more unread") {
		t.Fatalf("max_messages 1: oldest first, the rest stays unread: %s", got)
	}
	if got := tool(e, "1"); !strings.Contains(got, "third") {
		t.Fatalf("the rest arrives next time, not lost: %s", got)
	}
	e.prefs("1", core.PrefsReq{Broadcasts: core.ModeOff})
	err := e.c.Call(context.Background(), "send", core.SendReq{From: e.agent("2").ID, To: "project", Body: "noise"}, nil)
	if err == nil || !strings.Contains(err.Error(), "muted") {
		t.Fatalf("the sender learns nobody wants it: %v", err)
	}
	if got := tool(e, "1"); got != "" {
		t.Fatalf("broadcasts off: %s", got)
	}
}

// By default a long message is pushed as a gist with its size and where to
// read it whole; short ones arrive whole beside it.
func TestLongMailIsPushedAsAGist(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	long := "Cart review:\n" + strings.Repeat("rounding is off by a cent; ", 100)
	e.send("2", "1", long)
	e.send("2", "1", "short one")
	got := tool(e, "1")
	if !strings.Contains(got, "a gist of ~") || !strings.Contains(got, "for all of it") || !strings.Contains(got, "short one") || strings.Count(got, "rounding is off") > 15 {
		t.Fatalf("%s", got)
	}
}

// A long message beyond the character budget is cut with a pointer, and
// the next ones wait instead of being marked read and dropped.
func TestBudgetNeverLosesMessages(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("2", "codex", "SessionStart", nil)
	e.prefs("1", core.PrefsReq{MaxChars: 1000, MaxMessages: 10, InlineChars: 40000}) // whole bodies: the budget cuts
	e.send("2", "1", strings.Repeat("á", 900)) // multi-byte: the cut must not split a rune
	e.send("2", "1", "short one")
	got := tool(e, "1")
	if !strings.Contains(got, "get_thread") || strings.Contains(got, "short one") {
		t.Fatalf("first is cut with a pointer, second waits: %s", got)
	}
	if !strings.Contains(got, "á") || strings.Contains(got, "�") {
		t.Fatal("cut must keep valid UTF-8")
	}
	if got := tool(e, "1"); !strings.Contains(got, "short one") {
		t.Fatalf("second arrives next: %s", got)
	}
}

// The session-start briefing stays short with many peers and points at the board.
func TestBriefingIsCompact(t *testing.T) {
	e := setup(t)
	for i := 2; i < 30; i++ {
		e.as(itoa(i), "codex", "SessionStart", nil)
	}
	_, out := e.as("1", "claude-code", "SessionStart", nil)
	got := ctxOf(t, out)
	if !strings.Contains(got, "28 other agent(s)") || !strings.Contains(got, "+20 more") {
		t.Fatalf("briefing should list 8 and count the rest: %s", got)
	}
	if !strings.Contains(got, "create_task") || !strings.Contains(got, "set_status name=") {
		t.Fatalf("briefing should point at the board and at naming: %s", got)
	}
	t.Logf("briefing chars with 28 peers: %d", len(got))
	if len(got) > 2500 {
		t.Fatalf("briefing too long for 28 peers: %d chars", len(got))
	}
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}
