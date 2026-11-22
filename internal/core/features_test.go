package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

func inbox(t *testing.T, s *Store, id string) []Message {
	t.Helper()
	got, err := s.Inbox(context.Background(), InboxReq{Agent: id})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Group mail reaches each agent the way it chose: one-line briefs by
// default, full or nothing on request; named mail always in full.
func TestDeliveryModesFollowEachRecipientsPrefs(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	d := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", AnchorPID: 4})
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: c.ID, Broadcasts: ModeFull}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: d.ID, Broadcasts: ModeOff}); err != nil {
		t.Fatal(err)
	}
	m, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "reloading core"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Briefed, ",") != b.Name || strings.Join(m.Delivered, ",") != c.Name {
		t.Fatalf("modes wrong: brief=%v full=%v", m.Briefed, m.Delivered)
	}
	if got := inbox(t, s, d.ID); len(got) != 0 {
		t.Fatalf("broadcasts off must not deliver: %+v", got)
	}
	if got := inbox(t, s, b.ID); len(got) != 1 || got[0].Mode != ModeBrief {
		t.Fatalf("b should get a brief: %+v", got)
	}
	u, _ := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "stop pushing", Urgent: true})
	if strings.Join(u.Briefed, ",") != b.Name || strings.Join(u.Delivered, ",") != c.Name {
		t.Fatalf("an agent's urgent mail follows each recipient's choice: %+v", u)
	}
	dm, _ := s.Send(ctx, SendReq{From: a.ID, To: d.Name, Body: "just you"})
	if len(dm.Delivered) != 1 {
		t.Fatalf("named mail is full: %+v", dm)
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: b.ID, Broadcasts: "loud"}); err == nil {
		t.Fatal("bad mode must be refused")
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: b.ID, MaxChars: 100}); err == nil {
		t.Fatal("a tiny max_chars must be refused")
	}
}

func TestBranchAndTopicTargets(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", Branch: "feat/x", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", Branch: "feat/x", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", Branch: "main", AnchorPID: 3})
	other := reg(t, s, RegisterReq{Harness: "cursor", Project: "web", AnchorPID: 4})
	m, err := s.Send(ctx, SendReq{From: a.ID, To: "branch", Body: "rebasing feat/x"})
	if err != nil || strings.Join(m.Briefed, ",") != b.Name {
		t.Fatalf("branch should reach only b: %+v %v", m, err)
	}
	if m, _ := s.Send(ctx, SendReq{From: a.ID, To: "branch:main", Body: "x"}); strings.Join(m.Briefed, ",") != c.Name {
		t.Fatalf("branch:main should reach only c: %+v", m)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "#reload", Body: "x"}); err == nil {
		t.Fatal("a topic without subscribers has no recipients")
	}
	for _, x := range []Agent{c, other} {
		if _, err := s.SetPrefs(ctx, PrefsReq{Agent: x.ID, Subscribe: []string{"#Reload"}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: b.ID, Subscribe: []string{"reload:brief"}}); err != nil {
		t.Fatal(err)
	}
	m, err = s.Send(ctx, SendReq{From: a.ID, To: "#reload", Body: "core reload in 1 min"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Delivered, ",") != c.Name || strings.Join(m.Briefed, ",") != b.Name {
		t.Fatalf("topic: full to c, brief to b, nothing to other projects: %+v", m)
	}
	topics, _ := s.Topics(ctx, a.ProjectPath)
	if topics["reload"] != 2 {
		t.Fatalf("topics: %v", topics)
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: c.ID, Unsubscribe: []string{"reload"}}); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.GetPrefs(ctx, c.ID); len(p.Topics) != 0 {
		t.Fatalf("unsubscribe: %+v", p)
	}
}

// Old announcements aren't worth an agent's context: unread briefs expire,
// unread direct mail never does.
func TestStaleBriefsExpireDirectMailStays(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	_, _ = s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "old news"})
	_, _ = s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: "please review #12"})
	s.now = func() time.Time { return now.Add(BriefTTL + time.Minute) }
	_ = s.Register // keep both online despite the clock jump
	_, _ = s.db.Exec(`UPDATE agents SET last_seen=?`, s.ms())
	if n, _, _ := s.Unread(ctx, b.ID); n != 1 {
		t.Fatalf("only the direct message is still unread, got %d", n)
	}
	got := inbox(t, s, b.ID)
	if len(got) != 1 || got[0].Body != "please review #12" {
		t.Fatalf("inbox: %+v", got)
	}
	hist, _ := s.Inbox(ctx, InboxReq{Agent: b.ID, All: true})
	if len(hist) != 2 {
		t.Fatalf("history keeps the expired brief: %+v", hist)
	}
	// An offline agent whose only unread mail is stale briefs can be pruned.
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	_, _ = s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "more news"})
	_ = s.Leave(ctx, c.ID)
	s.now = func() time.Time { return now.Add(BriefTTL + Retention + time.Hour) }
	if err := s.Prune(ctx, Retention); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, c.ID); err == nil {
		t.Fatal("stale briefs must not keep a long-gone agent alive")
	}
}

// A read with a budget takes what fits and leaves the rest unread, instead
// of marking everything read and cutting it off.
func TestInboxBudgetLeavesTheRestUnread(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	for i := 0; i < 3; i++ {
		_, _ = s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: strings.Repeat("x", 1000)})
	}
	got, err := s.Inbox(ctx, InboxReq{Agent: b.ID, Budget: 2500})
	if err != nil || len(got) != 2 {
		t.Fatalf("budget 2500 fits two ~1160-char messages: %d %v", len(got), err)
	}
	if n, _, _ := s.Unread(ctx, b.ID); n != 1 {
		t.Fatalf("the third stays unread: %d", n)
	}
	one, _ := s.Inbox(ctx, InboxReq{Agent: b.ID, Budget: 10})
	if len(one) != 1 {
		t.Fatal("the first message is always taken, whatever the budget")
	}
}

// Automatic names aren't handed to a new session while peers may still
// address the old one; earlier names keep resolving after a rename.
func TestNamesAreNotRecycledAndOldNamesResolve(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	peer := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 9})
	_ = s.Leave(ctx, a.ID)
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 2})
	if b.Name == a.Name {
		t.Fatalf("a fresh session took %q, which peers may still address", a.Name)
	}
	renamed, err := s.SetStatus(ctx, StatusReq{ID: b.ID, Name: "w5-verif"})
	if err != nil || renamed.Name != "w5-verif" || len(renamed.Aka) != 1 || renamed.Aka[0] != b.Name {
		t.Fatalf("rename keeps the old name as aka: %+v %v", renamed, err)
	}
	m, err := s.Send(ctx, SendReq{From: peer.ID, To: b.Name, Body: "to your old name"})
	if err != nil || strings.Join(m.Delivered, ",") != "w5-verif" {
		t.Fatalf("old name should reach the renamed agent: %+v %v", m, err)
	}
}

// A new session (another IDE, provider or account) continues an offline
// agent: name, mail, threads, tasks, reservations and prefs move over.
func TestContinueAsHandsOverEverything(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	old := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Name: "ls6-cierre"})
	peer := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	q, _ := s.Send(ctx, SendReq{From: old.ID, To: peer.Name, Body: "can you check migrations?"})
	task, _ := s.CreateTask(ctx, TaskReq{By: peer.ID, Title: "HMAC pseudonyms", Assignee: old.Name})
	if _, err := s.Reserve(ctx, LockReq{Agent: old.ID, Paths: []string{"src/usage.py"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = s.SetPrefs(ctx, PrefsReq{Agent: old.ID, Broadcasts: ModeOff, Subscribe: []string{"reload"}})
	_ = s.Leave(ctx, old.ID)
	_, _ = s.Send(ctx, SendReq{From: peer.ID, ReplyTo: q.ID, Body: "checked: fine"})

	// Just gone may be a long think or a restart: not yet.
	early := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 4})
	if _, err := s.ContinueAs(ctx, early.ID, "ls6-cierre"); err == nil || !strings.Contains(err.Error(), "seen") {
		t.Fatalf("an agent gone seconds ago must not be continued yet: %v", err)
	}
	_ = s.Leave(ctx, early.ID)
	s.now = func() time.Time { return start.Add(handoverQuiet + time.Minute) }
	if _, err := s.SetStatus(ctx, StatusReq{ID: peer.ID, State: "working"}); err != nil {
		t.Fatal(err)
	}

	other := reg(t, s, RegisterReq{Harness: "cursor", Project: "web", AnchorPID: 5})
	if _, err := s.ContinueAs(ctx, other.ID, "ls6-cierre"); err == nil {
		t.Fatal("another project's agent must not continue it")
	}
	if _, err := s.ContinueAs(ctx, other.ID, peer.Name); err == nil {
		t.Fatal("an online agent can't be continued")
	}
	// Same declared project name, another checkout on this machine.
	fake := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", ProjectPath: "/elsewhere/api", AnchorPID: 6})
	if _, err := s.ContinueAs(ctx, fake.ID, "ls6-cierre"); err == nil {
		t.Fatal("a session in another checkout must not continue it")
	}
	// A machine name is only a claim: declaring another one doesn't get
	// around the same-checkout rule.
	spoof := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", ProjectPath: "/elsewhere/api", Machine: "laptop", AnchorPID: 8})
	if _, err := s.ContinueAs(ctx, spoof.ID, "ls6-cierre"); err == nil {
		t.Fatal("a claimed other machine must not bypass the checkout check")
	}
	human := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 7, Name: "human"})
	_ = s.Leave(ctx, human.ID)
	if _, err := s.ContinueAs(ctx, peer.ID, "human"); err == nil {
		t.Fatal("the operator's identity can't be taken over")
	}

	nu := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 3})
	got, err := s.SetStatus(ctx, StatusReq{ID: nu.ID, ContinueAs: "ls6-cierre", State: "working"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "ls6-cierre" {
		t.Fatalf("takes the old name: %+v", got)
	}
	mail := inbox(t, s, nu.ID)
	if len(mail) != 1 || mail[0].Body != "checked: fine" {
		t.Fatalf("unread mail moves: %+v", mail)
	}
	if th, err := s.Thread(ctx, nu.ID, q.ID); err != nil || len(th) != 2 {
		t.Fatalf("old threads stay readable: %v %d", err, len(th))
	}
	ts, _ := s.Threads(ctx, nu.ID, 10)
	if len(ts) != 1 || ts[0].ThreadID != q.ThreadID {
		t.Fatalf("threads list: %+v", ts)
	}
	tk, _ := s.ListTasks(ctx, TaskFilter{Agent: nu.ID, Mine: true})
	if len(tk) != 1 || tk[0].ID != task.ID {
		t.Fatalf("tasks move: %+v", tk)
	}
	if l, _ := s.CheckFile(ctx, peer.ID, "src/usage.py"); l == nil || l.AgentID != nu.ID {
		t.Fatalf("reservation moves: %+v", l)
	}
	if p, _ := s.GetPrefs(ctx, nu.ID); p.Broadcasts != ModeOff || p.Topics["reload"] != ModeFull {
		t.Fatalf("prefs move: %+v", p)
	}
	m, err := s.Send(ctx, SendReq{From: peer.ID, ReplyTo: q.ID, Body: "one more thing"})
	if err != nil || strings.Join(m.Delivered, ",") != "ls6-cierre" {
		t.Fatalf("replies to old messages reach the continuation: %+v %v", m, err)
	}
}

// The queue replaces "tell me when you let go of NEXT.md": the holder is
// told someone waits, and the file passes on by itself, first come first served.
func TestWaitQueueHandsFilesOver(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"NEXT.md"}}); err != nil {
		t.Fatal(err)
	}
	rb, err := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"NEXT.md"}, Wait: true, Reason: "my line TM20"})
	if err != nil || len(rb.Queued) != 1 || rb.Queued[0].Position != 1 || len(rb.Granted) != 0 {
		t.Fatalf("b should be queued first: %+v %v", rb, err)
	}
	rc, _ := s.Reserve(ctx, LockReq{Agent: c.ID, Paths: []string{"NEXT.md"}, Wait: true})
	if rc.Queued[0].Position != 2 {
		t.Fatalf("c is second: %+v", rc.Queued)
	}
	told := inbox(t, s, a.ID)
	if len(told) != 2 || told[0].From != SystemSender || !strings.Contains(told[0].Body, "my line TM20") {
		t.Fatalf("holder is told who waits and why: %+v", told)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, ReplyTo: told[0].ID, Body: "ok"}); err == nil {
		t.Fatal("replying to a hub notice must explain, not fail obscurely")
	}
	if _, err := s.Release(ctx, a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.CheckFile(ctx, c.ID, "NEXT.md"); l == nil || l.AgentID != b.ID {
		t.Fatalf("b gets it first: %+v", l)
	}
	if got := inbox(t, s, b.ID); len(got) != 1 || !strings.Contains(got[0].Body, "granted") {
		t.Fatalf("b is told: %+v", got)
	}
	if l, _ := s.CheckFile(ctx, a.ID, "NEXT.md"); l == nil || l.AgentID != b.ID {
		t.Fatal("c must not jump the queue")
	}
	// b's reservation expires: the next settle hands it to c.
	now := time.Now()
	s.now = func() time.Time { return now.Add(31 * time.Minute) }
	_, _ = s.db.Exec(`UPDATE agents SET last_seen=?`, s.ms())
	if l, _ := s.CheckFile(ctx, a.ID, "NEXT.md"); l == nil || l.AgentID != c.ID {
		t.Fatalf("expiry passes it to c: %+v", l)
	}
	if w, _ := s.Waiters(ctx, ""); len(w) != 0 {
		t.Fatalf("queue empty: %+v", w)
	}
}

// Reserving for a task claims it, and finishing the task lets go of the
// files (and hands them to whoever waits).
func TestTaskLinkedReservations(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	task, _ := s.CreateTask(ctx, TaskReq{By: b.ID, Title: "split invoices.py"})
	res, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"src/invoices.py"}, Task: task.ID})
	if err != nil || res.Task == nil || res.Task.Status != "doing" || res.Task.Assignee != a.Name {
		t.Fatalf("reserving for a todo task claims it: %+v %v", res, err)
	}
	if !strings.Contains(res.Granted[0].Reason, "split invoices.py") || res.Granted[0].TaskID != task.ID {
		t.Fatalf("reason defaults to the task: %+v", res.Granted[0])
	}
	if _, err := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/x.py"}, Task: task.ID}); err == nil {
		t.Fatal("someone else's doing task can't be reserved for")
	}
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/invoices.py"}, Wait: true})
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: a.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.CheckFile(ctx, a.ID, "src/invoices.py"); l == nil || l.AgentID != b.ID {
		t.Fatalf("done releases the task's files to the queue: %+v", l)
	}
}

// A plain reservation can't take files someone is already queued for, and
// queueing, leaving and queueing again tells the holder only once.
func TestQueueCannotBeJumpedOrUsedToSpam(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	c := reg(t, s, RegisterReq{Harness: "gemini", Project: "api", AnchorPID: 3})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"src/a.py"}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/**"}, Wait: true}); len(r.Queued) != 1 {
		t.Fatalf("b queues for src/**: %+v", r)
	}
	r, err := s.Reserve(ctx, LockReq{Agent: c.ID, Paths: []string{"src/b.py"}})
	if err != nil || len(r.Granted) != 0 {
		t.Fatalf("c must not slip in ahead of b's place in the queue: %+v %v", r, err)
	}
	for i := 0; i < 3; i++ {
		_, _ = s.Release(ctx, b.ID, nil)
		_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"src/**"}, Wait: true})
	}
	if told := inbox(t, s, a.ID); len(told) != 1 {
		t.Fatalf("holder told %d times, want once: %+v", len(told), told)
	}
}

// Reserving for a free task claims it; when no file was granted or queued
// the claim is undone, so the task isn't left "doing" by someone who has nothing.
func TestTaskClaimUndoneWhenNothingReserved(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	task, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "fix nav"})
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"nav.tsx"}}); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"nav.tsx"}, Task: task.ID})
	if err != nil || len(r.Granted) != 0 {
		t.Fatalf("nav.tsx is a's: %+v %v", r, err)
	}
	got, _ := s.ListTasks(ctx, TaskFilter{Agent: b.ID})
	for _, tk := range got {
		if tk.ID == task.ID && (tk.Status != "todo" || tk.AssigneeID != "") {
			t.Fatalf("claim must be undone: %+v", tk)
		}
	}
}

// An earlier name (aka) never intercepts mail for an agent that holds that
// name now, even an offline one.
func TestAkaNeverInterceptsTheCurrentHolder(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Name: "w1"})
	if _, err := s.SetStatus(ctx, StatusReq{ID: a.ID, Name: "bar"}); err != nil {
		t.Fatal(err)
	}
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Name: "w1"})
	if b.Name != "w1" {
		t.Skipf("explicit name not granted (%s); nothing to check", b.Name)
	}
	_ = s.Leave(ctx, b.ID)
	got, err := s.Resolve(ctx, "w1")
	if err != nil || got.ID != b.ID {
		t.Fatalf("w1 is b's name now, not a's aka: %+v %v", got, err)
	}
}

// The sender learns which recipients are between turns, so it doesn't sit
// waiting on a reply that comes only when the user resumes that session.
func TestSendSaysWhoIsAway(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "working"})
	if m, _ := s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: "ping"}); len(m.Away) != 0 {
		t.Fatalf("a working agent isn't away: %+v", m.Away)
	}
	_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "idle"})
	if m, _ := s.Send(ctx, SendReq{From: a.ID, To: b.Name, Body: "ping"}); strings.Join(m.Away, ",") != b.Name {
		t.Fatalf("an idle hook-only agent is away: %+v", m.Away)
	}
}

// A caller with a deadline (a hook) gives up on a busy database quickly
// instead of stalling the agent's tool call.
func TestBusyWaitIsBounded(t *testing.T) {
	path := t.TempDir() + "/hub.db"
	slow, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	a := reg(t, slow, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	fast, err := Open(path, Options{Busy: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer fast.Close()
	ctx := context.Background()
	conn, err := slow.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	_, err = fast.SetStatus(ctx, StatusReq{ID: a.ID, State: "working"})
	_, _ = conn.ExecContext(ctx, `ROLLBACK`)
	if err == nil || time.Since(t0) > 3*time.Second {
		t.Fatalf("want a quick busy error, got %v after %s", err, time.Since(t0))
	}
}

// A session that ends and comes straight back (/clear, compaction, an MCP
// restart) keeps its name, files and queue place, with no leave/join noise;
// one that really ended goes offline after the grace, even if its process
// is still running.
func TestDepartIsGracefulNotFlappy(t *testing.T) {
	path := t.TempDir() + "/hub.db"
	s, err := Open(path, Options{Stale: 45 * time.Second, Machine: "box",
		Alive: func(p []int) (map[int]bool, error) { return map[int]bool{1: true, 2: true}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	start := time.Now()
	s.now = func() time.Time { return start }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1, Machine: "box"})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Machine: "box"})
	_, _ = s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"NEXT.md"}})
	_, _ = s.Reserve(ctx, LockReq{Agent: b.ID, Paths: []string{"NEXT.md"}, Wait: true})
	events := func() int {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM events WHERE kind IN ('joined','left')`).Scan(&n)
		return n
	}
	before := events()

	_ = s.Depart(ctx, b.ID)
	s.now = func() time.Time { return start.Add(5 * time.Second) }
	again := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2, Machine: "box"})
	if again.ID != b.ID || again.State != "idle" || events() != before {
		t.Fatalf("a quick return is silent and keeps the identity: %+v, %d events", again, events()-before)
	}
	if w, _ := s.Waiters(ctx, ""); len(w) != 1 {
		t.Fatalf("queue place survives the flap: %+v", w)
	}

	_ = s.Depart(ctx, a.ID)
	s.now = func() time.Time { return start.Add(5*time.Second + departGrace + time.Second) }
	_, _ = s.SetStatus(ctx, StatusReq{ID: b.ID, State: "working"})
	if l, _ := s.CheckFile(ctx, a.ID, "NEXT.md"); l == nil || l.AgentID != b.ID {
		t.Fatalf("a's goodbye is real after the grace: NEXT.md passes to b: %+v", l)
	}
}

// An older binary resets the schema marker; reopening must not re-migrate
// or rewrite it (that would ping-pong writes on every hook), and a v1
// database upgrades in place keeping its data.
func TestMigrationFromV1AndOlderBinaries(t *testing.T) {
	path := t.TempDir() + "/hub.db"
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 1})
	// Make it look like a v1 database: drop the v2 additions.
	for _, q := range []string{`DROP TABLE prefs`, `DROP TABLE subscriptions`, `DROP TABLE waiters`,
		`ALTER TABLE receipts DROP COLUMN mode`, `ALTER TABLE agents DROP COLUMN aka`, `ALTER TABLE locks DROP COLUMN task_id`,
		`UPDATE meta SET value='1' WHERE key='schema'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatalf("upgrade from v1: %v", err)
	}
	if got, err := s.Get(context.Background(), a.ID); err != nil || got.Name != a.Name {
		t.Fatalf("data kept across the upgrade: %+v %v", got, err)
	}
	// An older binary writes its marker back.
	_, _ = s.db.Exec(`UPDATE meta SET value='1' WHERE key='schema'`)
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var have string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&have)
	if have != "1" {
		t.Fatalf("an already-upgraded database must be left alone, marker now %q", have)
	}
}

// Mail addressed by id (the "task done" notice to its creator) shows the
// recipient's name, not a raw id, in the feed and the dashboard.
func TestMailByIDShowsTheName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	task, _ := s.CreateTask(ctx, TaskReq{By: a.ID, Title: "split invoices"})
	if _, err := s.Claim(ctx, b.ID, task.ID); err != nil { // a task is finished by its holder
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, TaskUpdate{ID: task.ID, By: b.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	got := inbox(t, s, a.ID)
	if len(got) != 1 || got[0].Target != a.Name {
		t.Fatalf("notice to the creator is addressed by name: %+v", got)
	}
}
