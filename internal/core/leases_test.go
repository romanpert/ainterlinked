package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Editing reserves the file for the editor, renewed by each edit; another
// agent editing it is shown the holder; nobody jumps a queue by editing.
func TestEditingReservesTheFile(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Via: "hook"})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Via: "hook"})
	if l, err := s.TouchFile(ctx, a.ID, "src/app.py"); err != nil || l != nil {
		t.Fatalf("a free file is a's to edit: %+v %v", l, err)
	}
	l, _ := s.TouchFile(ctx, b.ID, "src/app.py")
	if l == nil || l.AgentID != a.ID || l.Reason != autoReason {
		t.Fatalf("b editing a's file sees a's automatic reservation: %+v", l)
	}
	s.now = func() time.Time { return start.Add(10 * time.Minute) }
	_, _ = s.SetStatus(ctx, StatusReq{ID: a.ID, State: "working"})
	_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "working"})
	_, _ = s.TouchFile(ctx, a.ID, "src/app.py")
	s.now = func() time.Time { return start.Add(20 * time.Minute) }
	_, _ = s.SetStatus(ctx, StatusReq{ID: a.ID, State: "working"})
	_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "working"})
	if l, _ := s.TouchFile(ctx, b.ID, "src/app.py"); l == nil || l.AgentID != a.ID {
		t.Fatalf("each edit renews the lease (would have ended at 15 min): %+v", l)
	}
	locks, _ := s.Locks(ctx, "")
	if len(locks) != 1 {
		t.Fatalf("renewing doesn't stack reservations: %+v", locks)
	}

	// b queues for a folder a is editing in; c editing another file there
	// must not slip in ahead of b.
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3, Via: "hook"})
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/"}, Wait: true})
	if l, _ := s.TouchFile(ctx, c.ID, "src/other.py"); l != nil {
		t.Fatalf("not held, so not refused: %+v", l)
	}
	for _, l := range mustLocks(t, s) {
		if l.AgentID == c.ID {
			t.Fatalf("editing must not take a file someone is queued for: %+v", l)
		}
	}
}

// A hook agent's reservations lapse IdleLapse after its turn ends (it is
// told), and the queue moves on; while it works, they hold; reservations
// of agents without hooks keep their TTL.
func TestIdleReservationsLapse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Via: "hook"})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Via: "hook"})
	m := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", AnchorPID: 3, Via: "mcp"})
	_, _ = s.SetStatus(ctx, StatusReq{ID: a.ID, State: "working"})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"NEXT.md"}, TTL: 180}); err != nil {
		t.Fatal(err)
	}
	// One file: folders lapse when unused (TestReservationsAreLeasesKeptByUse).
	_, _ = s.Reserve(ctx, LockReq{Agent: m.ID, Paths: []string{"docs/x.md"}, TTL: 180})
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"NEXT.md"}, Wait: true})

	beat := func(d time.Duration, idle bool) {
		s.now = func() time.Time { return start.Add(d) }
		st := "working"
		if idle {
			st = "idle"
		}
		_, _ = s.SetStatus(ctx, StatusReq{ID: a.ID, State: st})
		_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "working"})
		_, _ = s.SetStatus(ctx, StatusReq{ID: m.ID})
	}
	beat(30*time.Minute, false)
	if l, _ := s.CheckFile(ctx, b.ID, "NEXT.md"); l == nil || l.AgentID != a.ID {
		t.Fatalf("while a works, its 3 h reservation holds: %+v", l)
	}
	beat(31*time.Minute, true) // a's turn ends
	if l, _ := s.CheckFile(ctx, m.ID, "NEXT.md"); l == nil || l.LapsesAt == 0 {
		t.Fatalf("an idle holder's reservation says when it lapses: %+v", l)
	}
	beat(31*time.Minute+IdleLapse+time.Minute, true)
	if l, _ := s.CheckFile(ctx, m.ID, "NEXT.md"); l == nil || l.AgentID != b.ID {
		t.Fatalf("a's reservation lapsed and b, queued, got it: %+v", l)
	}
	told := inbox(t, s, a.ID)
	if len(told) == 0 || !strings.Contains(told[len(told)-1].Body, "lapsed") {
		t.Fatalf("a is told its reservation lapsed: %+v", told)
	}
	if l, _ := s.CheckFile(ctx, a.ID, "docs/x.md"); l == nil || l.AgentID != m.ID {
		t.Fatalf("an agent without hooks keeps its TTL: %+v", l)
	}
}

func mustLocks(t *testing.T, s *Store) []Lock {
	t.Helper()
	l, err := s.Locks(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return l
}
