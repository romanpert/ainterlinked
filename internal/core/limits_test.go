package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// limited is a store on the user's terms, with a clock the test moves and
// two hook agents kept online through it.
type limited struct {
	*Store
	t     *testing.T
	start time.Time
	a, b  Agent
}

func withLimits(t *testing.T, set func(*Limits)) *limited {
	t.Helper()
	l := DefaultLimits()
	set(&l)
	if err := l.Check(); err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	s.SetLimits(l)
	x := &limited{Store: s, t: t, start: time.Now()}
	s.now = func() time.Time { return x.start }
	x.a = reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Via: "hook"})
	x.b = reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Via: "hook"})
	x.at(0, "working")
	return x
}

// at moves the clock, with a in state aState and b working.
func (x *limited) at(d time.Duration, aState string) {
	x.t.Helper()
	x.now = func() time.Time { return x.start.Add(d) }
	for id, st := range map[string]string{x.a.ID: aState, x.b.ID: "working"} {
		if _, err := x.SetStatus(context.Background(), StatusReq{ID: id, State: st}); err != nil {
			x.t.Fatal(err)
		}
	}
}

func (x *limited) heldByA(file string) bool {
	x.t.Helper()
	l, err := x.CheckFile(context.Background(), x.b.ID, file)
	if err != nil {
		x.t.Fatal(err)
	}
	return l != nil && l.AgentID == x.a.ID
}

func (x *limited) reserve(by Agent, path string, wait bool) LockResult {
	x.t.Helper()
	r, err := x.Reserve(context.Background(), LockReq{Agent: by.ID, Paths: []string{path}, TTL: 24 * 60, Wait: wait})
	if err != nil {
		x.t.Fatal(err)
	}
	return r
}

func TestBroadcastLimitIsTheUsers(t *testing.T) {
	ctx := context.Background()
	send := func(x *limited) error {
		_, err := x.Send(ctx, SendReq{From: x.a.ID, To: "project", Body: "news"})
		return err
	}
	x := withLimits(t, func(l *Limits) { l.BroadcastBurst, l.BroadcastRefillMinutes = 1, 3 })
	if err := send(x); err != nil {
		t.Fatal(err)
	}
	if err := send(x); err == nil || !strings.Contains(err.Error(), "up to 1 broadcasts") || !strings.Contains(err.Error(), "every 3 minutes") {
		t.Fatalf("the refusal states the user's terms: %v", err)
	}
	x.at(3*time.Minute+time.Second, "working")
	if err := send(x); err != nil {
		t.Fatal(err)
	}
	for _, off := range []func(*Limits){func(l *Limits) { l.BroadcastBurst = 0 }, func(l *Limits) { l.BroadcastRefillMinutes = 0 }} {
		x := withLimits(t, off)
		for i := 0; i < 3*BroadcastBurst; i++ {
			if err := send(x); err != nil {
				t.Fatalf("off: %v", err)
			}
		}
	}
}

func TestAutoReserveIsTheUsers(t *testing.T) {
	ctx := context.Background()
	touch := func(x *limited) {
		t.Helper()
		if l, err := x.TouchFile(ctx, x.a.ID, "cart.go"); err != nil || l != nil {
			t.Fatalf("%+v %v", l, err)
		}
	}
	x := withLimits(t, func(l *Limits) { l.AutoReserveMinutes = 60 })
	touch(x)
	x.at(59*time.Minute, "working")
	if !x.heldByA("cart.go") {
		t.Fatal("an hour, as set")
	}
	x = withLimits(t, func(l *Limits) { l.AutoReserveMinutes = 0 })
	touch(x)
	if x.heldByA("cart.go") {
		t.Fatal("off: editing reserves nothing")
	}
	x.reserve(x.b, "cart.go", false)
	if l, _ := x.TouchFile(ctx, x.a.ID, "cart.go"); l == nil || l.AgentID != x.b.ID {
		t.Fatalf("off: another's reservation still holds: %+v", l)
	}
}

func TestIdleLapseIsTheUsers(t *testing.T) {
	x := withLimits(t, func(l *Limits) { l.IdleLapseMinutes = 2 })
	if r := x.reserve(x.a, "cart.go", false); !strings.Contains(r.Granted[0].Terms, "idle for 2m") {
		t.Fatalf("terms: %+v", r.Granted)
	}
	x.at(time.Minute, "idle")
	x.at(4*time.Minute, "idle")
	if x.heldByA("cart.go") {
		t.Fatal("two minutes, as set")
	}
	x = withLimits(t, func(l *Limits) { l.IdleLapseMinutes = 0 })
	if r := x.reserve(x.a, "cart.go", false); strings.Contains(r.Granted[0].Terms, "idle") {
		t.Fatalf("off: the terms don't mention it: %+v", r.Granted)
	}
	x.at(time.Minute, "idle")
	x.at(5*time.Hour, "idle")
	if l, _ := x.CheckFile(context.Background(), x.b.ID, "cart.go"); l == nil || l.LapsesAt != 0 {
		t.Fatalf("off: only its own term ends it: %+v", l)
	}
}

func TestUseWindowsAreTheUsers(t *testing.T) {
	x := withLimits(t, func(l *Limits) { l.FolderUseMinutes, l.ProjectUseMinutes = 1, 90 })
	x.reserve(x.a, "src/", false)
	x.at(2*time.Minute, "working")
	if x.heldByA("src/a.go") {
		t.Fatal("a folder: one minute, as set")
	}
	x.reserve(x.a, ".", false)
	x.at(80*time.Minute, "working")
	if !x.heldByA("docs/a.md") {
		t.Fatal("the project: ninety minutes, as set")
	}
	x = withLimits(t, func(l *Limits) { l.FolderUseMinutes, l.ProjectUseMinutes = 0, 0 })
	x.reserve(x.a, "src/", false)
	x.at(5*time.Hour, "working")
	if !x.heldByA("src/a.go") {
		t.Fatal("off: only its own term ends it")
	}
}

func TestAbandonedTaskTermIsTheUsers(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		minutes int
		back    bool
	}{{5, true}, {0, false}} {
		x := withLimits(t, func(l *Limits) { l.AbandonTaskMinutes = c.minutes })
		task, _ := x.CreateTask(ctx, TaskReq{By: x.b.ID, Title: "fix"})
		got, err := x.Claim(ctx, x.a.ID, task.ID)
		if err != nil || strings.Contains(got.Terms, "offline for over 5m") != c.back {
			t.Fatalf("%d: terms %q %v", c.minutes, got.Terms, err)
		}
		x.now = func() time.Time { return x.start.Add(10 * time.Minute) } // a is silent
		_, _ = x.SetStatus(ctx, StatusReq{ID: x.b.ID, State: "working"})
		if _, err := x.Claim(ctx, x.b.ID, task.ID); (err == nil) != c.back {
			t.Fatalf("%d minutes: claimable again = %v", c.minutes, err == nil)
		}
	}
}

func TestQueueTermsAreTheUsers(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		minutes int
		waits   bool
	}{{10, false}, {0, true}} {
		x := withLimits(t, func(l *Limits) { l.QueueMinutes = c.minutes })
		x.reserve(x.a, "cart.go", false)
		x.reserve(x.b, "cart.go", true)
		x.at(5*time.Hour, "working")
		if w, err := x.Waiters(ctx, x.a.ProjectPath); err != nil || (len(w) == 1) != c.waits {
			t.Fatalf("%d minutes: %+v %v", c.minutes, w, err)
		}
	}
	for _, c := range []struct {
		minutes int
		lasts   time.Duration
	}{{3, 3 * time.Minute}, {0, 24 * time.Hour}} {
		x := withLimits(t, func(l *Limits) { l.IdleGrantMinutes = c.minutes })
		x.reserve(x.b, "cart.go", false)
		x.reserve(x.a, "cart.go", true)
		x.at(time.Minute, "idle")
		if _, err := x.Release(ctx, x.b.ID, []string{"cart.go"}); err != nil {
			t.Fatal(err)
		}
		l, _ := x.CheckFile(ctx, x.b.ID, "cart.go")
		if l == nil || l.ExpiresAt-x.ms() != c.lasts.Milliseconds() {
			t.Fatalf("%d minutes: granted to an idle agent for %+v", c.minutes, l)
		}
	}
}

func TestBriefTermIsTheUsers(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		hours int
		shown bool
	}{{1, false}, {0, true}} {
		x := withLimits(t, func(l *Limits) { l.BriefHours = c.hours })
		if _, err := x.Send(ctx, SendReq{From: x.a.ID, To: "project", Body: "old news"}); err != nil {
			t.Fatal(err)
		}
		x.at(50*time.Hour, "working")
		if err := x.Prune(ctx, Retention); err != nil {
			t.Fatal(err)
		}
		if n, _, _ := x.Unread(ctx, x.b.ID); (n == 1) != c.shown {
			t.Fatalf("%d hours: unread %d", c.hours, n)
		}
	}
}

func TestLimitsCheck(t *testing.T) {
	if err := DefaultLimits().Check(); err != nil {
		t.Fatal(err)
	}
	if err := (Limits{}).Check(); err != nil {
		t.Fatalf("everything off is the user's right: %v", err)
	}
	l := DefaultLimits()
	l.QueueMinutes = -1
	if err := l.Check(); err == nil || !strings.Contains(err.Error(), "limits.queue_minutes") {
		t.Fatalf("a negative limit is refused by name: %v", err)
	}
	if l := (Limits{MaxHolds: 2}); l.HeldOut(1) || !l.HeldOut(2) || (Limits{}).HeldOut(99) {
		t.Fatal("HeldOut")
	}
}
