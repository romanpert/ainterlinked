package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/transcript"
)

const thread = "019a3c5e-0000-7000-8000-000000000001"

type wakeBox struct {
	t      *testing.T
	st     *core.Store
	w      *waker
	codex  core.Agent
	peer   core.Agent
	clock  time.Time
	queued []string
	fail   error
	over   bool
}

func newWakeBox(t *testing.T) *wakeBox {
	t.Helper()
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	b := &wakeBox{t: t, clock: time.Now()}
	dir := t.TempDir()
	st, err := core.Open(filepath.Join(dir, "hub.db"), core.Options{Stale: 24 * time.Hour, Now: func() time.Time { return b.clock }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b.st = st
	srv := NewServer(st)
	b.w = srv.newWaker()
	b.w.over = func(context.Context, string) bool { return b.over }
	b.w.queue = func(_ context.Context, th, text string) error {
		b.queued = append(b.queued, th+" "+text)
		return b.fail
	}
	reg := func(h, session string, pid int) core.Agent {
		a, err := b.st.Register(context.Background(), core.RegisterReq{Harness: h, Project: "shop", ProjectPath: "/w/shop", Machine: "box", AnchorPID: pid, SessionID: session, Via: "hook", Delivery: "turn"})
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	b.codex, b.peer = reg("codex", thread, 1), reg("claude-code", "c1", 2)
	b.state("idle")
	return b
}

func (b *wakeBox) state(s string) {
	b.t.Helper()
	if _, err := b.st.SetStatus(context.Background(), core.StatusReq{ID: b.codex.ID, State: s}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *wakeBox) send(to, body string) {
	b.t.Helper()
	if _, err := b.st.Send(context.Background(), core.SendReq{From: b.peer.ID, To: to, Body: body}); err != nil {
		b.t.Fatal(err)
	}
}

// round runs one a minute later and returns how many wake-ups it queued.
func (b *wakeBox) round() int {
	b.clock = b.clock.Add(time.Minute)
	n := len(b.queued)
	b.w.round(context.Background())
	return len(b.queued) - n
}

func TestTheHubWakesAnIdleCodexOnce(t *testing.T) {
	b := newWakeBox(t)
	if b.round() != 0 {
		t.Fatal("no mail, no wake-up")
	}
	b.send("project", "news for everyone")
	if b.round() != 0 {
		t.Fatal("group mail costs nobody a turn")
	}
	b.send(b.codex.Name, "SECRET-BODY please review")
	if b.round() != 1 {
		t.Fatal("mail by name wakes it")
	}
	// Other processes can read a command line: the mail isn't on it.
	if q := b.queued[0]; !strings.HasPrefix(q, thread+" "+transcript.WakeMark) || strings.Contains(q, "SECRET-BODY") {
		t.Fatalf("queued: %s", q)
	}
	if n, _ := b.st.PeerHeld(context.Background(), b.codex.ID); n != 1 {
		t.Fatalf("the turn is peers': %d", n)
	}
	if a, _ := b.st.Get(context.Background(), b.codex.ID); a.Delivery != "push" {
		t.Fatalf("it can be woken, and senders are told so: %s", a.Delivery)
	}
	if b.round() != 0 {
		t.Fatal("the same mail wakes it once")
	}
	b.send(b.codex.Name, "one more")
	if b.round() != 1 {
		t.Fatal("new mail wakes it again")
	}
}

func TestWhatKeepsACodexAsleep(t *testing.T) {
	for name, set := range map[string]func(*wakeBox){
		"it is working":   func(b *wakeBox) { b.state("working") },
		"it is over cap":  func(b *wakeBox) { b.over = true },
		"max_holds is up": func(b *wakeBox) { hold(b, b.codex.ID, core.DefaultLimits().MaxHolds) },
		"it reads by hand": func(b *wakeBox) {
			if _, err := b.st.SetPrefs(context.Background(), core.PrefsReq{Agent: b.codex.ID, Inject: core.InjectManual}); err != nil {
				b.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newWakeBox(t)
			b.send(b.codex.Name, "hi")
			set(b)
			if b.round() != 0 {
				t.Fatal("woken")
			}
		})
	}
}

func hold(b *wakeBox, id string, n int) {
	for i := 0; i < n; i++ {
		if _, err := b.st.MarkTurn(context.Background(), core.TurnReq{Agent: id}); err != nil {
			b.t.Fatal(err)
		}
	}
}

// A Codex that can't be woken is reported as such, and not tried over and over.
func TestAFailedWakeUp(t *testing.T) {
	b := newWakeBox(t)
	b.fail = errors.New("no such thread")
	b.send(b.codex.Name, "hi")
	if b.round() != 1 || b.round() != 0 {
		t.Fatal("tried once")
	}
	if a, _ := b.st.Get(context.Background(), b.codex.ID); a.Delivery != "turn" {
		t.Fatalf("delivery %s", a.Delivery)
	}
}

func TestCodexQueueTakesOnlyAThreadID(t *testing.T) {
	for _, id := range []string{"", "my session", "--remote", thread + " x", thread[:35]} {
		if err := codexQueue(context.Background(), id, "x"); err == nil || !strings.Contains(err.Error(), "thread id") {
			t.Errorf("%q: %v", id, err)
		}
	}
}

// Off until the user's config.toml says otherwise.
func TestWakingFollowsTheUsersConfig(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	srv, _ := startServer(t, func(int) (int, string) { return 4242, "codex" })
	stamp := srv.followLimits("")
	if srv.wakeIdle.Load() {
		t.Fatal("on with no config")
	}
	for _, on := range []string{"true", "false"} {
		if err := os.WriteFile(config.Path(), []byte("[delivery]\nwake_idle = "+on+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if stamp = srv.followLimits(stamp); srv.wakeIdle.Load() != (on == "true") {
			t.Fatalf("wake_idle = %s", on)
		}
	}
}
