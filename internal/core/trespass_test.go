package core

import (
	"context"
	"strings"
	"testing"
)

// Nobody can pin a trespass on someone else, and the writer can't clear
// its own: the holder it wrote over settles it.
func TestTrespassRecordsCantBeForgedOrSelfCleared(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	human := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 9, Name: "human"})
	holder := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	writer := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	smear := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})

	// The labels are the hub's own, for agents and for the human's CLI.
	for _, by := range []Agent{smear, human} {
		if _, err := s.CreateTask(ctx, TaskReq{By: by.ID, Title: "x", Labels: []string{"Restitution"}, Assignee: writer.Name}); err == nil {
			t.Fatalf("%s set a reserved label", by.Name)
		}
	}
	held, err := s.Reserve(ctx, LockReq{Agent: holder.ID, Paths: []string{"cart.js"}})
	if err != nil {
		t.Fatal(err)
	}
	lock := held.Granted[0].ID
	if _, err := s.RecordTrespass(ctx, TrespassReq{Writer: holder.ID, LockID: lock, File: "cart.js"}); err == nil {
		t.Fatal("writing your own file is no trespass")
	}
	if _, err := s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "other.js"}); err == nil {
		t.Fatal("the reservation must cover the file")
	}
	if _, err := s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock + 99, File: "cart.js"}); err == nil {
		t.Fatal("no such reservation")
	}
	task, err := s.RecordTrespass(ctx, TrespassReq{Writer: writer.ID, LockID: lock, File: "cart.js", Program: "sed"})
	if err != nil {
		t.Fatal(err)
	}
	if task.CreatedByID != holder.ID || task.AssigneeID != writer.ID || task.Labels[0] != LabelRestitution {
		t.Fatalf("owed by the writer to the holder: %+v", task)
	}
	// The writer can say what it did, not close, cancel or pass it on.
	for _, u := range []TaskUpdate{{Status: "done"}, {Status: "cancelled"}} {
		u.By, u.ID = writer.ID, task.ID
		if _, err := s.UpdateTask(ctx, u); err == nil || !strings.Contains(err.Error(), "owed to "+holder.Name) {
			t.Fatalf("writer %s: %v", u.Status, err)
		}
	}
	nobody := ""
	if _, err := s.UpdateTask(ctx, TaskUpdate{By: writer.ID, ID: task.ID, Assignee: &nobody}); err == nil {
		t.Fatal("writer unassigned itself")
	}
	// Nor can the holder pin it on a third agent.
	third := smear.Name
	if _, err := s.UpdateTask(ctx, TaskUpdate{By: holder.ID, ID: task.ID, Assignee: &third}); err == nil || !strings.Contains(err.Error(), "only the human") {
		t.Fatalf("holder reassigned it: %v", err)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{By: writer.ID, ID: task.ID, Status: "review", Result: "restored their version"}); err != nil {
		t.Fatal(err)
	}
	f, _ := s.AgentFacts(ctx, "", []string{writer.Name})
	if w := f[strings.ToLower(writer.Name)]; w.Trespass != 1 || w.TrespassOpen != 1 {
		t.Fatalf("open until the holder settles: %+v", w)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{By: holder.ID, ID: task.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	f, _ = s.AgentFacts(ctx, "", []string{writer.Name})
	if w := f[strings.ToLower(writer.Name)]; w.Trespass != 1 || w.TrespassOpen != 0 {
		t.Fatalf("settled: %+v", w)
	}
}
