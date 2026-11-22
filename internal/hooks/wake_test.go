package hooks

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

// waking turns wake_idle on and shortens the waiter's pace.
func waking(t *testing.T) *env {
	t.Helper()
	e := setup(t)
	grace, every := wakeGrace, wakeEvery
	wakeGrace, wakeEvery = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { wakeGrace, wakeEvery = grace, every })
	wakeIdle(t, e, true)
	return e
}

func wakeIdle(t *testing.T, e *env, on bool) {
	t.Helper()
	e.cfg.Delivery.WakeIdle = on
	if err := config.Save(e.cfg); err != nil {
		t.Fatal(err)
	}
}

// me is the test's own process: the waiter checks its session still runs.
func me() string { return strconv.Itoa(os.Getpid()) }

// watch starts the waiter of the session anchored here.
func (e *env) watch(ctx context.Context) chan string {
	e.t.Helper()
	e.t.Setenv("AINTERLINKED_ANCHOR_PID", me())
	out := make(chan string, 1)
	go func() {
		s, err := waitForMail(ctx, e.cfg, e.c, "claude-code", Input{Cwd: e.dir})
		if err != nil {
			s = "error: " + err.Error()
		}
		out <- s
	}()
	return out
}

func ended(t *testing.T, ch chan string) string {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("the waiter is still there")
		return ""
	}
}

func quiet(t *testing.T, ch chan string) {
	t.Helper()
	select {
	case s := <-ch:
		t.Fatalf("the waiter ended: %q", s)
	case <-time.After(200 * time.Millisecond):
	}
}

func (e *env) idleClaude() {
	e.as("2", "codex", "SessionStart", nil)
	e.as(me(), "claude-code", "SessionStart", nil)
	e.as(me(), "claude-code", "Stop", nil)
}

func TestMailWakesAnIdleSession(t *testing.T) {
	e := waking(t)
	e.idleClaude()
	ch := e.watch(context.Background())
	quiet(t, ch)
	e.send("2", me(), "your review is ready")
	s := ended(t, ch)
	if !strings.HasPrefix(s, transcript.WakeMark) || !strings.Contains(s, "your review is ready") {
		t.Fatalf("woken with: %q", s)
	}
	id := e.agent(me()).ID
	if Holds(id) != 1 {
		t.Fatalf("a wake-up counts against max_holds: %d", Holds(id))
	}
	if n, _ := e.c.Store.PeerHeld(context.Background(), id); n != 1 {
		t.Fatalf("the turn is peers': %d", n)
	}
	// If Claude Code runs its prompt hook for the wake-up, it isn't the user's.
	e.as(me(), "claude-code", "UserPromptSubmit", map[string]any{"prompt": s})
	if n, _ := e.c.Store.PeerHeld(context.Background(), id); n != 1 || Holds(id) != 1 {
		t.Fatalf("a wake-up passed as the user's prompt (held %d, holds %d)", n, Holds(id))
	}
	e.as(me(), "claude-code", "UserPromptSubmit", map[string]any{"prompt": "go on"})
	if Holds(id) != 0 {
		t.Fatal("the user's prompt starts over")
	}
}

func TestTheWaiterEndsQuietly(t *testing.T) {
	for name, act := range map[string]func(*env){
		"the session works again": func(e *env) {
			e.as(me(), "claude-code", "UserPromptSubmit", map[string]any{"prompt": "go"})
		},
		"the session ends":  func(e *env) { e.as(me(), "claude-code", "SessionEnd", nil) },
		"a newer one waits": func(e *env) { takeWatch(e.agent(me()).ID) },
		"waking is off now": func(e *env) {
			cfg := e.cfg
			cfg.Delivery.WakeIdle = false
			_ = config.Save(cfg)
			e.send("2", me(), "hi")
		},
		"max_holds is used up": func(e *env) {
			for i := 0; i < e.cfg.Limits.MaxHolds; i++ {
				BumpHolds(e.agent(me()).ID)
			}
			e.send("2", me(), "hi")
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := waking(t)
			e.idleClaude()
			ch := e.watch(context.Background())
			quiet(t, ch)
			act(e)
			if s := ended(t, ch); s != "" {
				t.Fatalf("woke with %q", s)
			}
		})
	}
}

// Group mail, and mail its agent reads by hand, never cost a turn.
func TestOnlyMailByNameWakes(t *testing.T) {
	e := waking(t)
	e.idleClaude()
	ctx, cancel := context.WithCancel(context.Background())
	ch := e.watch(ctx)
	if err := e.c.Call(ctx, "send", core.SendReq{From: e.agent("2").ID, To: "project", Body: "news", Urgent: true}, nil); err != nil {
		t.Fatal(err)
	}
	quiet(t, ch)
	id := e.agent(me()).ID
	if err := e.c.Call(ctx, "set_prefs", map[string]any{"agent": id, "inject": core.InjectManual}, nil); err != nil {
		t.Fatal(err)
	}
	e.send("2", me(), "by name, but it reads by hand")
	quiet(t, ch)
	cancel()
	ended(t, ch)
	var u hub.UnreadResult
	_ = e.c.Call(context.Background(), "unread", hub.AgentParam{Agent: id}, &u)
	if u.Direct != 1 {
		t.Fatalf("mail taken: %+v", u)
	}
}

// Off unless the user turns it on.
func TestTheWaiterNeedsTheUser(t *testing.T) {
	e := waking(t)
	e.idleClaude()
	e.send("2", me(), "hi")
	inTerminal(t)
	e.cfg.Delivery.WakeIdle = false
	run := func() (int, string) {
		var out bytes.Buffer
		return Wait(context.Background(), e.cfg, "claude-code", strings.NewReader("{}"), &out), out.String()
	}
	if code, out := run(); code != 0 || out != "" {
		t.Fatalf("off: exit %d, %q", code, out)
	}
	e.cfg.Delivery.WakeIdle = true
	e.cfg.Harnesses["claude-code"] = config.HarnessCfg{}
	if code, out := run(); code != 0 || out != "" {
		t.Fatalf("muted: exit %d, %q", code, out)
	}
}

// inTerminal: the hook runs inside `claude` in a terminal.
func inTerminal(t *testing.T) {
	t.Helper()
	name, args := anchorName, programArgs
	t.Cleanup(func() { anchorName, programArgs = name, args })
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	anchorName = func() string { return "claude" }
	programArgs = func() ([]string, error) { return []string{"claude", "fix the tests"}, nil }
}

// Anywhere the hook would run in the foreground, the session would hang
// until mail came: it must not wait there.
func TestTheWaiterOnlyWaitsInTheBackground(t *testing.T) {
	inTerminal(t)
	stream := []string{"claude", "--output-format", "stream-json", "--input-format", "stream-json"}
	for name, c := range map[string]struct {
		anchor, entry string
		args          []string
		err           error
		want          bool
	}{
		"a terminal":                    {"claude", "cli", []string{"claude", "--resume", "x"}, nil, true},
		"VS Code":                       {"claude", "claude-vscode", stream, nil, true},
		"the desktop app":               {"claude", "claude-desktop", stream, nil, true},
		"claude -p":                     {"claude", "sdk-cli", []string{"claude", "-p", "hi"}, nil, false},
		"claude -p run by a session":    {"claude", "cli", []string{"claude", "-p", "hi"}, nil, false},
		"... by one in VS Code":         {"claude", "claude-vscode", []string{"claude", "hi", "--print"}, nil, false},
		"-p together with another flag": {"claude", "cli", []string{"claude", "-cp", "hi"}, nil, false},
		"-p that streams":               {"claude", "claude-vscode", append([]string{"claude", "-p"}, stream[1:]...), nil, false},
		"an SDK script":                 {"claude", "sdk-ts", stream, nil, false},
		"Cursor opened by a session":    {"cursor", "cli", []string{"cursor", "."}, nil, false},
		"no command line":               {"claude", "cli", nil, errors.New("denied"), false},
	} {
		t.Setenv("CLAUDE_CODE_ENTRYPOINT", c.entry)
		anchorName = func() string { return c.anchor }
		programArgs = func() ([]string, error) { return c.args, c.err }
		if got := waitsInBackground(); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// stuck is a hub that takes calls and never answers.
type stuck struct {
	hub.Client
	on atomic.Bool
}

func (s *stuck) Call(ctx context.Context, method string, params, out any) error {
	if s.on.Load() {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.Client.Call(ctx, method, params, out)
}

func TestAStuckHubDoesNotKeepAWaiter(t *testing.T) {
	e := waking(t)
	old := wakeCall
	wakeCall = 50 * time.Millisecond
	t.Cleanup(func() { wakeCall = old })
	e.idleClaude()
	s := &stuck{Client: e.c}
	e.t.Setenv("AINTERLINKED_ANCHOR_PID", me())
	ch := make(chan string, 1)
	go func() {
		text, _ := waitForMail(context.Background(), e.cfg, s, "claude-code", Input{Cwd: e.dir})
		ch <- text
	}()
	quiet(t, ch)
	s.on.Store(true)
	if out := ended(t, ch); out != "" {
		t.Fatalf("woke with %q", out)
	}
}

// Codex is woken by a queued prompt: it is peers', and counts as a hold.
func TestACodexWakeUpIsNotTheUsersPrompt(t *testing.T) {
	e := setup(t)
	e.as("1", "codex", "SessionStart", nil)
	e.as("2", "claude-code", "SessionStart", nil)
	e.send("2", "1", "look at this")
	id := e.agent("1").ID
	if _, err := e.c.Store.MarkTurn(context.Background(), core.TurnReq{Agent: id}); err != nil { // as the hub does
		t.Fatal(err)
	}
	_, out := e.as("1", "codex", "UserPromptSubmit", map[string]any{"prompt": transcript.WakeMark + " 1 new message(s)"})
	h, _ := out["hookSpecificOutput"].(map[string]any)
	if s, _ := h["additionalContext"].(string); !strings.Contains(s, "look at this") {
		t.Fatalf("the mail comes with the prompt: %v", out)
	}
	if n, _ := e.c.Store.PeerHeld(context.Background(), id); n != 1 || Holds(id) != 1 {
		t.Fatalf("held %d, holds %d", n, Holds(id))
	}
	// The user prompts while the next one is on its way.
	e.as("1", "codex", "UserPromptSubmit", map[string]any{"prompt": "do this"})
	e.as("1", "codex", "UserPromptSubmit", map[string]any{"prompt": transcript.WakeMark + " 1 new message(s)"})
	if n, _ := e.c.Store.PeerHeld(context.Background(), id); n != 1 {
		t.Fatalf("a woken turn passed as the user's (%d)", n)
	}
}
