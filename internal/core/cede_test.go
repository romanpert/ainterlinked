package core

import (
	"context"
	"strings"
	"testing"
)

func TestCedeHandsAReservationToSomeoneWaiting(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"cart.js"}, TTL: 20}); err != nil {
		t.Fatal(err)
	}
	// Nobody asked: no cession; the receiver's consent is its queue place.
	if _, err := s.Cede(ctx, a.ID, c.Name, nil); err == nil || !strings.Contains(err.Error(), "isn't queued") {
		t.Fatalf("cession to someone not waiting: %v", err)
	}
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"cart.js"}, Wait: true})
	_, _ = s.Reserve(ctx, LockReq{Agent: c.ID, Paths: []string{"cart.js"}, Wait: true})
	before, _ := s.CheckFile(ctx, b.ID, "cart.js")
	// The holder picks c, though b is first in line: the title is a's to give.
	n, err := s.Cede(ctx, a.ID, c.Name, []string{"cart.js"})
	if err != nil || n != 1 {
		t.Fatalf("cede: %d %v", n, err)
	}
	l, _ := s.CheckFile(ctx, b.ID, "cart.js")
	if l == nil || l.AgentID != c.ID || l.ExpiresAt != before.ExpiresAt {
		t.Fatalf("c holds it, for what was left of a's term: %+v (before %+v)", l, before)
	}
	got := inbox(t, s, c.ID)
	if len(got) == 0 || !strings.Contains(got[len(got)-1].Body, a.Name+" passed you their reservation of cart.js") {
		t.Fatalf("c is told: %+v", got)
	}
	w, _ := s.Waiters(ctx, a.ProjectPath)
	if len(w) != 1 || w[0].AgentID != b.ID {
		t.Fatalf("c left the queue, b still waits: %+v", w)
	}
	if _, err := s.Cede(ctx, a.ID, b.Name, []string{"cart.js"}); err == nil {
		t.Fatal("a no longer holds it")
	}
	if _, err := s.Cede(ctx, c.ID, c.Name, nil); err == nil {
		t.Fatal("to oneself")
	}
}
