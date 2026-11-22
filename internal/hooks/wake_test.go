package hooks

import (
	"bytes"
	"context"
	"os"
	"strconv"
	"strings"
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

// Off unless the user turns it on, and only under Claude Code: any other
// harness that runs Claude's hook files would wait in the foreground.
func TestTheWaiterNeedsTheUserAndClaude(t *testing.T) {
	e := waking(t)
	e.idleClaude()
	e.send("2", me(), "hi")
	old := anchorName
	t.Cleanup(func() { anchorName = old })
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "")
	for name, set := range map[string]func(){
		"off":     func() { anchorName = func() string { return "claude" }; e.cfg.Delivery.WakeIdle = false },
		"vs code": func() { anchorName = func() string { return "code" }; e.cfg.Delivery.WakeIdle = true },
		"a Codex": func() { anchorName = func() string { return "codex" } },
		"disabled": func() {
			anchorName = func() string { return "claude" }
			e.cfg.Harnesses["claude-code"] = config.HarnessCfg{}
		},
	} {
		set()
		var out bytes.Buffer
		if code := Wait(context.Background(), e.cfg, "claude-code", strings.NewReader("{}"), &out); code != 0 || out.Len() > 0 {
			t.Errorf("%s: exit %d, %q", name, code, out.String())
		}
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
}
