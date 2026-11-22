package core

import (
	"context"
	"testing"
)

// The queue goes by the owner's valuation, then by arrival: a waiter on a
// task the owner (a human) made urgent and gave to that waiter goes first.
// An agent's own priorities don't let it jump anyone.
func TestQueueFollowsTheOwnersPriority(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	owner := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 9, Name: "human"})
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	d := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", AnchorPID: 4})
	urgent := 0
	mine, err := s.CreateTask(ctx, TaskReq{By: owner.ID, Title: "hotfix", Priority: &urgent, Assignee: c.Name})
	if err != nil {
		t.Fatal(err)
	}
	selfMade, err := s.CreateTask(ctx, TaskReq{By: d.ID, Title: "my own urgent thing", Priority: &urgent, Assignee: d.Name})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"NEXT.md"}}); err != nil {
		t.Fatal(err)
	}
	rb, _ := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"NEXT.md"}, Wait: true})
	if rb.Queued[0].Position != 1 {
		t.Fatalf("b first in: %+v", rb.Queued)
	}
	rd, _ := s.Reserve(ctx, LockReq{Agent: d.ID, Paths: []string{"NEXT.md"}, Wait: true, Task: selfMade.ID})
	if rd.Queued[0].Position != 2 {
		t.Fatalf("an agent's own urgency doesn't jump the queue: %+v", rd.Queued)
	}
	rc, _ := s.Reserve(ctx, LockReq{Agent: c.ID, Paths: []string{"NEXT.md"}, Wait: true, Task: mine.ID})
	if rc.Queued[0].Position != 1 {
		t.Fatalf("the owner's urgent task goes first: %+v", rc.Queued)
	}
	if _, err := s.Release(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.CheckFile(ctx, b.ID, "NEXT.md"); l == nil || l.AgentID != c.ID {
		t.Fatalf("c gets it first: %+v", l)
	}
	if _, err := s.Release(ctx, c.ID, nil); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.CheckFile(ctx, d.ID, "NEXT.md"); l == nil || l.AgentID != b.ID {
		t.Fatalf("then first come: b: %+v", l)
	}
	w, _ := s.Waiters(ctx, a.ProjectPath)
	if len(w) != 1 || w[0].AgentID != d.ID {
		t.Fatalf("d still waits: %+v", w)
	}
}

// Linking someone else's urgent task doesn't move a waiter up either.
func TestQueueIgnoresAnotherAgentsTask(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	owner := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 9, Name: "human"})
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	urgent := 0
	forA, _ := s.CreateTask(ctx, TaskReq{By: owner.ID, Title: "hotfix", Priority: &urgent, Assignee: a.Name})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"x.go"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"x.go"}, Wait: true})
	rc, _ := s.Reserve(ctx, LockReq{Agent: c.ID, Paths: []string{"x.go"}, Wait: true, Task: forA.ID})
	if len(rc.Queued) == 1 && rc.Queued[0].Position != 2 {
		t.Fatalf("c can't borrow a's priority: %+v", rc.Queued)
	}
}
