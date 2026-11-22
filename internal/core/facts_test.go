package core

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestAgentFactsAreWhatHappenedNotAScore(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	owner := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 9, Name: "human"})
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	quiet := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})

	t1, _ := s.CreateTask(ctx, TaskReq{By: owner.ID, Title: "one"})
	t2, _ := s.CreateTask(ctx, TaskReq{By: owner.ID, Title: "two"})
	_, _ = s.CreateTask(ctx, TaskReq{By: owner.ID, Title: "nobody's"})
	if _, err := s.Claim(ctx, a.ID, t1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Claim(ctx, a.ID, t2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{By: a.ID, ID: t1.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	// Shell writes to others' files: one on b's own initiative, one on the
	// user's order (the user's act: not b's fact).
	held, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"cart.js"}})
	if err != nil {
		t.Fatal(err)
	}
	lock := held.Granted[0].ID
	if _, err := s.RecordTrespass(ctx, TrespassReq{Writer: b.ID, LockID: lock, File: "cart.js", Program: "sed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordTrespass(ctx, TrespassReq{Writer: b.ID, LockID: lock, File: "cart.js", Program: "sed", ByUser: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.Send(ctx, SendReq{From: b.ID, To: "project", Body: "heads up"})
	_, _ = s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: "review #1?"})

	got, err := s.AgentFacts(ctx, a.ID, []string{a.Name, b.Name, quiet.Name})
	if err != nil {
		t.Fatal(err)
	}
	fa, fb := got[strings.ToLower(a.Name)], got[strings.ToLower(b.Name)]
	if fa.Taken != 2 || fa.Done != 1 || fa.Open != 1 || fa.Trespass != 0 {
		t.Fatalf("a: %+v", fa)
	}
	if fb.Trespass != 1 || fb.TrespassOpen != 1 || fb.Taken != 0 || !reflect.DeepEqual(fb.Holds, []string{"src/**"}) || fb.Broadcasts != 1 || fb.UnreadFromViewer != 1 {
		t.Fatalf("b: %+v", fb)
	}
	if _, ok := got[strings.ToLower(quiet.Name)]; ok {
		t.Fatalf("no facts, no line: %+v", got)
	}
}
