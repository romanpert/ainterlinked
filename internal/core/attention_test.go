package core

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The recipient's attention is its own: an agent's urgent mail is marked
// urgent but follows each recipient's delivery choice. Only the human's
// urgent mail reaches those who muted broadcasts, and in full.
func TestUrgentFollowsTheRecipientsChoice(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	briefed := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	muted := reg(t, s, RegisterReq{Harness: "cursor", Project: "api", AnchorPID: 3})
	human := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 4})
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: muted.ID, Broadcasts: ModeOff}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: human.ID, Broadcasts: ModeOff}); err != nil {
		t.Fatal(err)
	}

	m, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "stop pushing", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Briefed, ",") != briefed.Name || len(m.Delivered) != 0 {
		t.Fatalf("an agent's urgent mail follows each recipient's choice: %+v", m)
	}
	if got := inbox(t, s, briefed.ID); len(got) != 1 || !got[0].Urgent || got[0].Mode != ModeBrief {
		t.Fatalf("still marked urgent, as the brief it chose: %+v", got)
	}
	if got := inbox(t, s, muted.ID); len(got) != 0 {
		t.Fatalf("broadcasts off holds against an agent's urgent mail: %+v", got)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "harness:cursor", Body: "you, now", Urgent: true}); err == nil || !strings.Contains(err.Error(), "muted") {
		t.Fatalf("urgent is no way around a mute; the sender is told to name the agent: %v", err)
	}

	h, err := s.Send(ctx, SendReq{From: human.ID, To: "project", Body: "everyone stop", Urgent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Delivered) != 3 || len(h.Briefed) != 0 {
		t.Fatalf("the human's urgent mail reaches everyone in full, even muted: %+v", h)
	}
	if got := inbox(t, s, muted.ID); len(got) != 1 || got[0].Mode != ModeFull {
		t.Fatalf("the muted agent gets the human's urgent mail: %+v", got)
	}
}

// Only mail addressed to an agent by name or id may hold its turn open; the
// hub marks it so. Group mail, even urgent or delivered in full, never does.
func TestOnlyDirectMailIsMarkedToHoldATurn(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: b.ID, Broadcasts: ModeFull, Subscribe: []string{"reload"}}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"project", "#reload", "harness:codex", "*"} {
		if _, err := s.Send(ctx, SendReq{From: a.ID, To: to, Body: "group: " + to, Urgent: true}); err != nil {
			t.Fatalf("%s: %v", to, err)
		}
	}
	total, _, full, direct, err := s.UnreadByMode(ctx, b.ID)
	if err != nil || total != 4 || full != 4 || direct != 0 {
		t.Fatalf("group mail in full is not direct: total=%d full=%d direct=%d %v", total, full, direct, err)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: b.Name + ",project", Body: "you and the rest"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: b.ID, Body: "by id"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, direct, _ = s.UnreadByMode(ctx, b.ID); direct != 2 {
		t.Fatalf("named once, by name or id, makes it direct: %d", direct)
	}
	got, err := s.Inbox(ctx, InboxReq{Agent: b.ID, Direct: true, Limit: 50})
	if err != nil || len(got) != 2 || got[0].Body != "you and the rest" || got[1].Body != "by id" {
		t.Fatalf("direct=true reads only the mail addressed to it: %+v %v", got, err)
	}
	if left, _, _, _, _ := s.UnreadByMode(ctx, b.ID); left != 4 {
		t.Fatalf("the group mail stays unread for the next turn: %d", left)
	}
}

// Each sender has a broadcast budget that refills with time. When it is
// spent the send is refused, stating the rule and when the next one is
// possible. Direct mail, #topic and branch are never limited; the human
// isn't either.
func TestBroadcastsComeFromABudgetThatRefills(t *testing.T) {
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
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", Branch: "main", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "codex", Project: "api", Branch: "main", AnchorPID: 2})
	human := reg(t, s, RegisterReq{Harness: "human", Project: "api", AnchorPID: 3})
	if _, err := s.SetPrefs(ctx, PrefsReq{Agent: b.ID, Subscribe: []string{"reload"}}); err != nil {
		t.Fatal(err)
	}
	targets := []string{"project", "*", "project:api", "harness:codex", "all", "team"}
	for i := 0; i < BroadcastBurst; i++ {
		if _, err := s.Send(ctx, SendReq{From: a.ID, To: targets[i%len(targets)], Body: "news"}); err != nil {
			t.Fatalf("broadcast %d of %d: %v", i+1, BroadcastBurst, err)
		}
	}
	before, _ := s.RecentMessages(ctx, 100)
	_, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "one more", Urgent: true})
	if err == nil {
		t.Fatal("a spent budget refuses the broadcast, urgent or not")
	}
	for _, want := range []string{"attention is its own", "again in " + BroadcastRefill.String(), "#topic", "name the agents"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must state %q: %v", want, err)
		}
	}
	if after, _ := s.RecentMessages(ctx, 100); len(after) != len(before) {
		t.Fatal("a refused broadcast is not stored")
	}
	for _, to := range []string{b.Name, "#reload", "branch", "branch:main"} {
		if _, err := s.Send(ctx, SendReq{From: a.ID, To: to, Body: "still fine"}); err != nil {
			t.Fatalf("%s is never limited: %v", to, err)
		}
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "branch:feature-x", Body: "another branch's crowd"}); err == nil {
		t.Fatal("branch:<another branch> reaches agents who didn't choose the sender: it is a broadcast")
	}
	for i := 0; i < 2*BroadcastBurst; i++ {
		if _, err := s.Send(ctx, SendReq{From: human.ID, To: "project", Body: "from the human"}); err != nil {
			t.Fatalf("the human is exempt: %v", err)
		}
	}
	if _, err := s.Send(ctx, SendReq{From: b.ID, To: "project", Body: "my own budget"}); err != nil {
		t.Fatalf("the budget is per sender: %v", err)
	}

	at(BroadcastRefill+time.Second, a, b)
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "refilled one"}); err != nil {
		t.Fatalf("one broadcast is back after BroadcastRefill: %v", err)
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "too soon"}); err == nil {
		t.Fatal("only one came back")
	}
	at(BroadcastRefill*BroadcastBurst*2, a, b)
	for i := 0; i < BroadcastBurst; i++ {
		if _, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "after a quiet hour"}); err != nil {
			t.Fatalf("a quiet sender gets its whole budget back, no more: %v", err)
		}
	}
	if _, err := s.Send(ctx, SendReq{From: a.ID, To: "project", Body: "past the burst"}); err == nil {
		t.Fatal("quiet time doesn't bank more than BroadcastBurst")
	}
}

// A database marked with the previous schema version gets the broadcast
// budget and the direct-mail column on open, keeping its data.
func TestMigrationAddsTheAttentionSchema(t *testing.T) {
	path := t.TempDir() + "/hub.db"
	s, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 1})
	for _, q := range []string{`DROP TABLE broadcast_budget`, `ALTER TABLE receipts DROP COLUMN direct`,
		`UPDATE meta SET value='3' WHERE key='schema'`} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	s.Close()
	s, err = Open(path, Options{})
	if err != nil {
		t.Fatalf("upgrade from v3: %v", err)
	}
	defer s.Close()
	if !upgraded(s.db) {
		t.Fatal("v3 database not upgraded in place")
	}
	if got, err := s.Get(context.Background(), a.ID); err != nil || got.Name != a.Name {
		t.Fatalf("data kept across the upgrade: %+v %v", got, err)
	}
}
