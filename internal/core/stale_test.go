package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clockAt points s at a clock the test moves with the returned function.
func clockAt(s *Store, start time.Time) func(time.Duration) {
	now := start
	s.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

const testDay = 24 * time.Hour

// notices returns the hub's notices waiting for agent, leaving them unread.
func notices(t *testing.T, s *Store, agent string) []Message {
	t.Helper()
	all, err := s.Inbox(context.Background(), InboxReq{Agent: agent, Peek: true, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	var out []Message
	for _, m := range all {
		if m.FromID == SystemSender {
			out = append(out, m)
		}
	}
	return out
}

func listed(t *testing.T, s *Store, board string) map[int64]Task {
	t.Helper()
	list, err := s.ListTasks(context.Background(), TaskFilter{Board: board})
	if err != nil {
		t.Fatal(err)
	}
	out := map[int64]Task{}
	for _, x := range list {
		out[x.ID] = x
	}
	return out
}

// An open task shows how long nobody touched it, from a whole day on;
// a closed one never does.
func TestAnOpenTaskShowsItsAgeFromADay(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	open, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "open"})
	closed, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "closed"})
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: closed.ID, By: a.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	move(testDay - time.Minute)
	if got := listed(t, s, "shop")[open.ID].Untouched; got != 0 {
		t.Fatalf("under a day shows no age, got %d", got)
	}
	move(time.Minute)
	if got := listed(t, s, "shop")[open.ID].Untouched; got != 1 {
		t.Fatalf("a whole day untouched: %d", got)
	}
	move(20 * testDay)
	l := listed(t, s, "shop")
	if l[open.ID].Untouched != 21 || l[closed.ID].Untouched != 0 {
		t.Fatalf("open 21d, closed none: %d %d", l[open.ID].Untouched, l[closed.ID].Untouched)
	}
}

// Off by default: no notice, no count, however old the board.
func TestNothingIsSentWhileStaleAfterDaysIsZero(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	human := reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	if _, err := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "old"}); err != nil {
		t.Fatal(err)
	}
	move(400 * testDay)
	if n, err := s.NoticeStaleTasks(ctx); n != 0 || err != nil {
		t.Fatalf("off sends nothing: %d %v", n, err)
	}
	if len(notices(t, s, a.ID))+len(notices(t, s, human.ID)) != 0 {
		t.Fatal("a notice was sent with the setting off")
	}
	b, _ := s.Boards(ctx)
	if b[0].Stale != 0 || b[0].StaleDays != 0 {
		t.Fatalf("no count while off: %+v", b[0])
	}
}

// One notice per task and untouched stretch, grouped per recipient; any
// update starts a new stretch.
func TestOneNoticePerTaskUntilItIsUpdated(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetStaleAfterDays(14)
	creator := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	t1, _ := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "first"})
	t2, _ := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "second"})
	move(14*testDay - time.Minute)
	if n, _ := s.NoticeStaleTasks(ctx); n != 0 {
		t.Fatalf("not yet 14 days: %d noticed", n)
	}
	move(time.Minute)
	if n, err := s.NoticeStaleTasks(ctx); n != 2 || err != nil {
		t.Fatalf("both are 14 days old: %d %v", n, err)
	}
	if n, _ := s.NoticeStaleTasks(ctx); n != 0 {
		t.Fatalf("a second look sends nothing more: %d", n)
	}
	got := notices(t, s, creator.ID)
	if len(got) != 1 || !strings.Contains(got[0].Body, "#1 [todo]") || !strings.Contains(got[0].Body, "#2 [todo]") || !strings.Contains(got[0].Subject, "2 task(s)") {
		t.Fatalf("one message to the creator listing both: %+v", got)
	}
	if !strings.Contains(got[0].Body, "nothing is cancelled automatically") {
		t.Fatalf("the notice says nothing is cancelled: %q", got[0].Body)
	}
	if direct, _ := s.Inbox(ctx, InboxReq{Agent: creator.ID, Peek: true, Direct: true}); len(direct) != 0 {
		t.Fatalf("news for the next turn: it neither holds nor wakes a session: %+v", direct)
	}
	b, _ := s.Boards(ctx)
	if b[0].Stale != 2 || b[0].StaleDays != 14 {
		t.Fatalf("the board counts them: %+v", b[0])
	}
	prio := 1
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: t1.ID, By: creator.ID, Priority: &prio}); err != nil {
		t.Fatal(err)
	}
	move(14 * testDay)
	if n, _ := s.NoticeStaleTasks(ctx); n != 1 {
		t.Fatalf("only the updated task starts a new stretch: %d", n)
	}
	got = notices(t, s, creator.ID)
	var second *Message
	for i := range got {
		if strings.Contains(got[i].Subject, "1 task(s)") {
			second = &got[i]
		}
	}
	if len(got) != 2 || second == nil || !strings.Contains(second.Body, "#1 [todo]") || strings.Contains(second.Body, "#2 ") {
		t.Fatalf("the second notice is about #1 alone: %+v", got)
	}
	_ = t2
}

// A review waits for a person: it goes to the human. So does a task whose
// creator is gone. A task in progress is abandon's, never noticed here.
func TestReviewAndOrphanedTasksGoToTheHumanAndDoingIsLeftAlone(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetStaleAfterDays(3)
	creator := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	gone := reg(t, s, RegisterReq{Harness: "gemini", Project: "shop", AnchorPID: 3})
	human := reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	review, _ := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "check me"})
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: review.ID, By: creator.ID, Status: "review"}); err != nil {
		t.Fatal(err)
	}
	orphan, _ := s.CreateTask(ctx, TaskReq{By: gone.ID, Title: "orphan"})
	doing, _ := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "in progress"})
	if _, err := s.Claim(ctx, creator.ID, doing.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM agents WHERE id=?`, gone.ID); err != nil {
		t.Fatal(err)
	}
	move(10 * testDay)
	if n, err := s.NoticeStaleTasks(ctx); n != 2 || err != nil {
		t.Fatalf("the review and the orphan, not the task in progress: %d %v", n, err)
	}
	if got := notices(t, s, creator.ID); len(got) != 0 {
		t.Fatalf("the creator hears nothing about a review or a task in progress: %+v", got)
	}
	got := notices(t, s, human.ID)
	if len(got) != 1 {
		t.Fatalf("one message to the human: %+v", got)
	}
	body := got[0].Body
	if !strings.Contains(body, "#1 [review]") || !strings.Contains(body, "#2 [todo] P2 orphan") || strings.Contains(body, "in progress") {
		t.Fatalf("the human's notice: %q", body)
	}
	if !strings.Contains(body, "ainterlinked tasks stale") || !strings.Contains(body, "task cancel") {
		t.Fatalf("the human is told the commands: %q", body)
	}
	_ = orphan
}

// With no human yet nobody hears about an orphan: it is not marked, and the
// human gets it once it exists.
func TestAnOrphanWaitsForTheHuman(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetStaleAfterDays(1)
	gone := reg(t, s, RegisterReq{Harness: "gemini", Project: "shop", AnchorPID: 3})
	if _, err := s.CreateTask(ctx, TaskReq{By: gone.ID, Title: "orphan"}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.db.Exec(`DELETE FROM agents WHERE id=?`, gone.ID)
	move(2 * testDay)
	if n, _ := s.NoticeStaleTasks(ctx); n != 0 {
		t.Fatalf("nobody to tell: %d", n)
	}
	human := reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	if n, _ := s.NoticeStaleTasks(ctx); n != 1 || len(notices(t, s, human.ID)) != 1 {
		t.Fatalf("the human hears once it exists: %d", n)
	}
}

// A creator that went offline would never read its notice, and the unread
// notice would pin its row forever: the human hears instead. A creator
// still online hears about its own tasks.
func TestAnOfflineCreatorsTasksGoToTheHuman(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetStaleAfterDays(3)
	online := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	offline := reg(t, s, RegisterReq{Harness: "gemini", Project: "shop", AnchorPID: 3})
	human := reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	if _, err := s.CreateTask(ctx, TaskReq{By: online.ID, Title: "mine"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, TaskReq{By: offline.ID, Title: "left behind"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(ctx, offline.ID); err != nil {
		t.Fatal(err)
	}
	move(10 * testDay)
	if n, err := s.NoticeStaleTasks(ctx); n != 2 || err != nil {
		t.Fatalf("both tasks are noticed: %d %v", n, err)
	}
	if got := notices(t, s, offline.ID); len(got) != 0 {
		t.Fatalf("an offline creator gets nothing it would never read: %+v", got)
	}
	got := notices(t, s, online.ID)
	if len(got) != 1 || !strings.Contains(got[0].Body, "#1 [todo]") || strings.Contains(got[0].Body, "#2 ") {
		t.Fatalf("the online creator hears about its own task alone: %+v", got)
	}
	got = notices(t, s, human.ID)
	if len(got) != 1 || !strings.Contains(got[0].Body, "#2 [todo] P2 left behind") || strings.Contains(got[0].Body, "#1 ") {
		t.Fatalf("the human hears about the offline creator's task: %+v", got)
	}
}

// An offline creator and no human yet: the task waits, unmarked, and the
// human gets it once it exists.
func TestAnOfflineCreatorsTaskWaitsForTheHuman(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetStaleAfterDays(1)
	creator := reg(t, s, RegisterReq{Harness: "gemini", Project: "shop", AnchorPID: 3})
	if _, err := s.CreateTask(ctx, TaskReq{By: creator.ID, Title: "left behind"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Leave(ctx, creator.ID); err != nil {
		t.Fatal(err)
	}
	move(2 * testDay)
	if n, _ := s.NoticeStaleTasks(ctx); n != 0 || len(notices(t, s, creator.ID)) != 0 {
		t.Fatalf("nobody online to tell: %d", n)
	}
	human := reg(t, s, RegisterReq{Harness: "human", Name: "human", AnchorPID: 2})
	if n, _ := s.NoticeStaleTasks(ctx); n != 1 || len(notices(t, s, human.ID)) != 1 {
		t.Fatalf("the human hears once it exists: %d", n)
	}
}

// `tasks stale`: oldest first, the days asked for (else the user's
// setting, else 14), tasks in progress too, closed ones never.
func TestStaleTasksListsOldestFirst(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	move := clockAt(s, time.Now())
	s.SetLimits(Limits{}) // nothing goes back to todo on its own here
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	young, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "young"}) // #1, touched again below
	oldest, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "oldest"})
	move(5 * testDay)
	mid, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "mid", Board: "other"})
	if _, err := s.Claim(ctx, a.ID, mid.ID); err != nil {
		t.Fatal(err)
	}
	done, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "done"})
	_, _ = s.UpdateTask(ctx, TaskUpdate{ID: done.ID, By: a.ID, Status: "done"})
	move(5 * testDay)
	prio := 0
	_, _ = s.UpdateTask(ctx, TaskUpdate{ID: young.ID, By: a.ID, Priority: &prio})
	move(5 * testDay) // oldest 15d, mid 10d, young 5d, done closed

	ids := func(l StaleList) []int64 {
		var out []int64
		for _, x := range l.Tasks {
			out = append(out, x.ID)
		}
		return out
	}
	l, err := s.StaleTasks(ctx, StaleReq{Days: 5})
	if err != nil || l.Days != 5 || len(l.Tasks) != 3 || l.Tasks[0].ID != oldest.ID || l.Tasks[1].ID != mid.ID || l.Tasks[2].ID != young.ID {
		t.Fatalf("oldest first, in progress included, closed left out: %v %v", ids(l), err)
	}
	if l.Tasks[0].Untouched != 15 {
		t.Fatalf("each with its age: %d", l.Tasks[0].Untouched)
	}
	if l, _ = s.StaleTasks(ctx, StaleReq{}); l.Days != DefaultStaleDays || len(l.Tasks) != 1 || l.Tasks[0].ID != oldest.ID {
		t.Fatalf("default 14 days: %d %v", l.Days, ids(l))
	}
	s.SetStaleAfterDays(9)
	if l, _ = s.StaleTasks(ctx, StaleReq{}); l.Days != 9 || len(l.Tasks) != 2 {
		t.Fatalf("the user's setting is the default: %d %v", l.Days, ids(l))
	}
	if l, _ = s.StaleTasks(ctx, StaleReq{Days: 1, Board: "other"}); len(l.Tasks) != 1 || l.Tasks[0].ID != mid.ID {
		t.Fatalf("one board: %v", ids(l))
	}
}

// A v8 database gains the column in place and keeps its tasks.
func TestAV8DatabaseGetsTheStaleColumnAndKeepsItsTasks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 1})
	task, _ := s.CreateTask(context.Background(), TaskReq{By: a.ID, Title: "kept"})
	for _, q := range []string{`ALTER TABLE tasks DROP COLUMN stale_noticed`, `UPDATE meta SET value='8' WHERE key='schema'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()
	s, err = Open(path, Options{StaleAfterDays: 1})
	if err != nil {
		t.Fatalf("upgrade from v8: %v", err)
	}
	defer s.Close()
	if ok, err := hasColumn(s.db, "tasks", "stale_noticed"); !ok || err != nil {
		t.Fatalf("column added: %v %v", ok, err)
	}
	var have string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&have)
	if have != "9" {
		t.Fatalf("marked v9, got %q", have)
	}
	if l := listed(t, s, "shop"); l[task.ID].Title != "kept" {
		t.Fatalf("the task survived: %+v", l)
	}
	if s.StaleAfterDays() != 1 {
		t.Fatal("Options.StaleAfterDays applies")
	}
	clockAt(s, time.Now().Add(2*testDay))
	if n, err := s.NoticeStaleTasks(context.Background()); n != 1 || err != nil {
		t.Fatalf("an old task is noticed after the upgrade: %d %v", n, err)
	}
}
