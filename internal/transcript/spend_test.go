package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reply(min int, id string, in, cw, cr, out int64) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d}},"timestamp":%q}`,
		id, in, cw, cr, out, at(min))
}

func hold(min int, ids ...int) string {
	var b strings.Builder
	b.WriteString("Stop hook feedback:\nNew messages from peer agents arrived while you were finishing.\n\n[ainterlinked] messages from peer agents:\n")
	for _, id := range ids {
		fmt.Fprintf(&b, "<<peer-message #%d from codex@shop to claude@shop, 2s ago | fence ab>>\nplease\n<<end #%d>>\n", id, id)
	}
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q},"isMeta":true,"timestamp":%q}`, b.String(), at(min))
}

func appendTo(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
}

// What a Claude Code session spent, by whose turn it was: the user's, or
// the peers' whose mail held it open. A reply streamed over several entries
// counts once; a subagent's spend belongs to the turn it ran in; later
// reads only add what the harness wrote since.
func TestSpendIsReadFromTheHarnessAndAttributedToWhoseTurnItWas(t *testing.T) {
	main := write(t,
		human(0, "build the cart"),
		reply(1, "m1", 10, 1000, 0, 5),
		reply(1, "m1", 10, 1000, 0, 50), // the same reply, streamed further
		reply(2, "m2", 3, 0, 1000, 20),
		hold(3, 15, 12),
		reply(4, "m3", 7, 100, 900, 30),
	)
	subs := filepath.Join(strings.TrimSuffix(main, ".jsonl"), "subagents")
	_ = os.MkdirAll(subs, 0o700)
	appendTo(t, filepath.Join(subs, "agent-a1.jsonl"), reply(1, "s1", 100, 0, 0, 10), reply(5, "s2", 200, 0, 0, 20))

	sp := NewSpender()
	l, ok := sp.Read("claude-code", sid)
	if !ok {
		t.Fatal("not found")
	}
	own, peers := l[CauseOwn], l["#12,#15"]
	if own != (Tokens{In: 113, CacheWrite: 1000, CacheRead: 1000, Out: 80}) {
		t.Fatalf("the user's turn: %+v", own)
	}
	if peers != (Tokens{In: 207, CacheWrite: 100, CacheRead: 900, Out: 50}) {
		t.Fatalf("the turn held for #12 and #15: %+v (ledger %+v)", peers, l)
	}
	if own.Billed() != 1193 {
		t.Fatalf("billed: %d", own.Billed())
	}

	// The user prompts again; only the new lines are read.
	appendTo(t, main, human(6, "now the tests"), reply(7, "m4", 1, 0, 0, 1))
	l, _ = sp.Read("claude-code", sid)
	if l[CauseOwn].Out != 81 || l["#12,#15"].Out != 50 {
		t.Fatalf("after the next prompt: %+v", l)
	}
	// A line the harness is still writing waits for the next read.
	f, _ := os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o600)
	half := reply(8, "m5", 0, 0, 0, 1000)
	f.WriteString(half[:40])
	f.Close()
	if l, _ = sp.Read("claude-code", sid); l[CauseOwn].Out != 81 {
		t.Fatalf("a half-written line was counted: %+v", l)
	}
	f, _ = os.OpenFile(main, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(half[40:] + "\n")
	f.Close()
	if l, _ = sp.Read("claude-code", sid); l[CauseOwn].Out != 1081 {
		t.Fatalf("the finished line: %+v", l)
	}
	// A fresh reader (a restarted hub) gets the same totals from the start.
	if again, _ := NewSpender().Read("claude-code", sid); again[CauseOwn] != l[CauseOwn] || again["#12,#15"] != l["#12,#15"] {
		t.Fatalf("a fresh read differs: %+v vs %+v", again, l)
	}
}

func TestCodexSpendIsItsLastRunningTotal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	const cx = "01a0585e-96d9-7f82-b86b-4d64d874a624"
	dir := filepath.Join(home, ".codex", "sessions", "2026", "11", "22")
	_ = os.MkdirAll(dir, 0o700)
	tc := func(in, cached, out int64) string {
		return fmt.Sprintf(`{"timestamp":"2026-11-22T10:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d,"total_tokens":0}},"rate_limits":{}}}`, in, cached, out)
	}
	appendTo(t, filepath.Join(dir, "rollout-2026-11-22T10-00-00-"+cx+".jsonl"),
		tc(1000, 400, 10), `{"type":"event_msg","payload":{"type":"token_count","info":null}}`, tc(47007, 26112, 482))
	l, ok := NewSpender().Read("codex", cx)
	if !ok || l[CauseOwn] != (Tokens{In: 20895, CacheRead: 26112, Out: 482}) {
		t.Fatalf("codex: %+v %v", l, ok)
	}
	if _, ok := NewSpender().Read("codex", "no-such-session"); ok {
		t.Fatal("a missing session was found")
	}
}

// Against a real transcript on this machine, when asked:
// IL_REAL_SESSION=<claude session id> go test ./internal/transcript -run Real -v
func TestRealSessionSpend(t *testing.T) {
	sid := os.Getenv("IL_REAL_SESSION")
	if sid == "" {
		t.Skip("IL_REAL_SESSION not set")
	}
	l, ok := NewSpender().Read("claude-code", sid)
	if !ok {
		t.Fatal("not found")
	}
	var total Tokens
	for k, v := range l {
		t.Logf("%-12q in %d, cache write %d, cache read %d, out %d (billed %d)", k, v.In, v.CacheWrite, v.CacheRead, v.Out, v.Billed())
		total = total.Add(v)
	}
	t.Logf("total billed %d, cache read %d", total.Billed(), total.CacheRead)
}
