package core

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A held task is its holder's: another agent can't cancel, reassign or edit
// it. Its creator and the human can. A free task can still be claimed by
// anyone through update_task, but not cancelled by a stranger.
func TestTasksBelongToTheirHolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	creator := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api"})
	holder := reg(t, s, RegisterReq{Harness: "codex", Project: "api"})
	other := reg(t, s, RegisterReq{Harness: "gemini", Project: "api"})
	human := reg(t, s, RegisterReq{Harness: "human", Project: "api"})

	task, err := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "migrate tokens"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Claim(ctx, holder.ID, task.ID)
	if err != nil || got.Terms == "" {
		t.Fatalf("claiming states the terms: %+v %v", got, err)
	}
	me := "me"
	for _, u := range []TaskUpdate{
		{ID: task.ID, By: other.ID, Status: "cancelled"},
		{ID: task.ID, By: other.ID, Assignee: &me},
		{ID: task.ID, By: other.ID, Title: "renamed"},
	} {
		if _, err := s.UpdateTask(ctx, u); err == nil || !strings.Contains(err.Error(), "belongs to") {
			t.Fatalf("a stranger changing a held task must be refused, got %v for %+v", err, u)
		}
	}
	body := "context for whoever does it"
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: creator.ID, Body: &body}); err != nil {
		t.Fatalf("the creator keeps a say: %v", err)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: human.ID, Priority: new(int)}); err != nil {
		t.Fatalf("the human can always change it: %v", err)
	}
	if done, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: holder.ID, Status: "done"}); err != nil || done.Status != "done" {
		t.Fatalf("the holder finishes it: %+v %v", done, err)
	}

	free, _ := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "rate-limit login"})
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: free.ID, By: other.ID, Status: "cancelled"}); err == nil {
		t.Fatal("a stranger must not cancel someone else's free task")
	}
	took, err := s.UpdateTask(ctx, TaskUpdate{ID: free.ID, By: other.ID, Status: "doing"})
	if err != nil || took.AssigneeID != other.ID || took.Status != "doing" {
		t.Fatalf("anyone may still claim a free task through update_task: %+v %v", took, err)
	}
}

// Wanting a task is one deduplicated counter, and its holder hears about it
// only at 1, 3, 9… wanters: many requests never become many messages.
func TestWantingATaskDoesNotFloodItsHolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	holder := reg(t, s, RegisterReq{Harness: "codex", Project: "api"})
	task, _ := s.CreateTask(ctx, TaskReq{By: holder.ID, Title: "fix rounding"})
	if _, err := s.Claim(ctx, holder.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.WantTask(ctx, holder.ID, task.ID); err == nil {
		t.Fatal("wanting your own task makes no sense")
	}
	const wanters = 12
	for i := 0; i < wanters; i++ {
		w := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", Name: fmt.Sprintf("w%d", i)})
		for k := 0; k < 3; k++ { // asking again changes nothing
			if _, err := s.WantTask(ctx, w.ID, task.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	list, _ := s.ListTasks(ctx, TaskFilter{Agent: holder.ID})
	if len(list) != 1 || list[0].Wanted != wanters {
		t.Fatalf("the task shows %d wanters once each: %+v", wanters, list)
	}
	mail, _ := s.Inbox(ctx, InboxReq{Agent: holder.ID, Limit: 200})
	notices := 0
	for _, m := range mail {
		if strings.Contains(m.Subject, "is wanted") {
			notices++
		}
	}
	if notices != 3 { // at 1, 3 and 9 wanters
		t.Fatalf("12 wanters x 3 requests must make 3 notices, got %d", notices)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: holder.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListTasks(ctx, TaskFilter{Agent: holder.ID}); list[0].Wanted != 0 {
		t.Fatalf("a finished task drops its wanters: %+v", list[0])
	}
}

// An explicit reservation is a lease: its terms are stated when it is made,
// it lasts while edits happen inside it, and it lapses after a window
// without use that is shorter the more it covers. Its holder is told.
func TestReservationsAreLeasesKeptByUse(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	at := func(d time.Duration, agents ...Agent) {
		s.now = func() time.Time { return start.Add(d) }
		for _, a := range agents {
			_, _ = s.SetStatus(ctx, StatusReq{ID: a.ID, State: "working"})
		}
	}
	at(0)
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", Via: "hook"})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", Via: "hook"})

	res, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"src/", "README.md", "."}, TTL: 120})
	if err != nil || len(res.Granted) != 3 {
		t.Fatalf("%+v %v", res, err)
	}
	for _, g := range res.Granted {
		if g.Terms == "" || (DefaultLimits().UseWindow(g.Pattern) > 0 && !strings.Contains(g.Terms, DefaultLimits().UseWindow(g.Pattern).String())) {
			t.Fatalf("the terms are stated up front: %+v", g)
		}
	}

	// The whole project lapses first (10 min unused); the rest still hold.
	at(ProjectUse+time.Minute, a, b)
	if l, _ := s.TouchFile(ctx, b.ID, "docs/guide.md"); l != nil {
		t.Fatalf("an unused ** must not fence the project off: %+v", l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "src/app.go"); l == nil || l.AgentID != a.ID {
		t.Fatalf("src/** is still within its window: %+v", l)
	}

	// a edits inside src/ at 20 min: that renews the folder lease.
	at(20*time.Minute, a, b)
	if l, _ := s.TouchFile(ctx, a.ID, "src/app.go"); l != nil {
		t.Fatalf("a edits its own folder: %+v", l)
	}
	at(45*time.Minute, a, b)
	if l, _ := s.TouchFile(ctx, b.ID, "src/app.go"); l == nil || l.AgentID != a.ID {
		t.Fatalf("used at 20 min, so held until 50: %+v", l)
	}

	at(51*time.Minute, a, b)
	if l, _ := s.TouchFile(ctx, b.ID, "src/app.go"); l != nil {
		t.Fatalf("unused for 30 min, src/ is free: %+v", l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "README.md"); l == nil || l.AgentID != a.ID {
		t.Fatalf("one file is individual use: it keeps its TTL while a works: %+v", l)
	}
	mail, _ := s.Inbox(ctx, InboxReq{Agent: a.ID, Limit: 50})
	lapsed := 0
	for _, m := range mail {
		if strings.Contains(m.Subject, "lapsed (unused)") {
			lapsed++
		}
	}
	if lapsed != 2 { // ** and src/**, not README.md
		t.Fatalf("the holder is told when a lease lapses on its terms: %d notices %+v", lapsed, mail)
	}
}

// A task whose holder has been offline for over AbandonAfter goes back to
// todo with its notes kept, and the holder is told; before that it stays.
func TestAbandonedTaskGoesBackToTodo(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	holder := reg(t, s, RegisterReq{Harness: "codex", Project: "api"})
	other := reg(t, s, RegisterReq{Harness: "gemini", Project: "api"})
	task, _ := s.CreateTask(ctx, TaskReq{By: other.ID, Title: "fix rounding"})
	if _, err := s.Claim(ctx, holder.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: holder.ID, Result: "half done: see branch fix/rounding"}); err != nil {
		t.Fatal(err)
	}

	s.now = func() time.Time { return start.Add(AbandonAfter / 2) }
	_, _ = s.SetStatus(ctx, StatusReq{ID: other.ID, State: "working"})
	if _, err := s.Claim(ctx, other.ID, task.ID); err == nil {
		t.Fatal("offline for less than AbandonAfter: still the holder's")
	}

	s.now = func() time.Time { return start.Add(AbandonAfter + time.Minute) }
	_, _ = s.SetStatus(ctx, StatusReq{ID: other.ID, State: "working"})
	got, err := s.Claim(ctx, other.ID, task.ID)
	if err != nil || got.AssigneeID != other.ID || got.Result == "" {
		t.Fatalf("abandoned task is claimable again, notes kept: %+v %v", got, err)
	}
	mail, _ := s.Inbox(ctx, InboxReq{Agent: holder.ID, Limit: 50})
	told := false
	for _, m := range mail {
		told = told || strings.Contains(m.Subject, "back to todo")
	}
	if !told {
		t.Fatalf("the former holder is told: %+v", mail)
	}
}
