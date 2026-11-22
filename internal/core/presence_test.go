package core

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// A session that only has hooks (no MCP heartbeat) is silent between tool
// calls. While its harness process is alive on this machine it must stay
// online and keep its name, its reservations and its mail.
func TestQuietAgentWithLiveProcessStaysOnline(t *testing.T) {
	live := map[int]bool{100: true}
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"), Options{
		Stale:   45 * time.Second,
		Machine: "box",
		Alive: func(pids []int) (map[int]bool, error) {
			out := map[int]bool{}
			for _, p := range pids {
				out[p] = live[p]
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }

	quiet := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 100, Via: "hook"})
	gone := reg(t, s, RegisterReq{Harness: "codex", Project: "app", AnchorPID: 200, Via: "hook"})
	remote := reg(t, s, RegisterReq{Harness: "cursor", Project: "app", AnchorPID: 100, Machine: "other", Via: "hook"})
	if _, err := s.Reserve(ctx, LockReq{Agent: quiet.ID, Paths: []string{"db/"}}); err != nil {
		t.Fatal(err)
	}

	// A long command: five minutes without a tool call.
	s.now = func() time.Time { return now.Add(5 * time.Minute) }
	list, _ := s.List(ctx, AgentFilter{})
	online := map[string]bool{}
	for _, a := range list {
		online[a.ID] = true
	}
	if !online[quiet.ID] {
		t.Fatal("agent whose process is alive went offline")
	}
	if online[gone.ID] {
		t.Fatal("agent whose process exited should be offline")
	}
	if online[remote.ID] {
		t.Fatal("a pid on another machine says nothing about that agent")
	}

	// Someone new joins the same project: it must not take the quiet agent's name.
	newcomer := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 300, Via: "hook"})
	if newcomer.Name == quiet.Name {
		t.Fatalf("newcomer took %q from a live session", quiet.Name)
	}
	back := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 100, Via: "hook"})
	if back.ID != quiet.ID || back.Name != quiet.Name {
		t.Fatalf("quiet agent changed identity: %+v -> %+v", quiet, back)
	}

	// Its reservation still holds against others.
	other := reg(t, s, RegisterReq{Harness: "gemini", Project: "app", AnchorPID: 400, Via: "hook"})
	s.now = func() time.Time { return now.Add(10 * time.Minute) }
	if l, err := s.CheckFile(ctx, other.ID, "db/schema.sql"); err != nil || l == nil || l.AgentID != quiet.ID {
		t.Fatalf("reservation of a quiet live agent not enforced: %+v %v", l, err)
	}

	// The process check has a ceiling: a pid can be reused after a crash.
	s.now = func() time.Time { return now.Add(10*time.Minute + anchorGrace) }
	list, _ = s.List(ctx, AgentFilter{})
	for _, a := range list {
		if a.ID == quiet.ID {
			t.Fatal("anchor grace should expire")
		}
	}
}

// When the process table can't be read, presence falls back to heartbeats.
func TestPresenceFallsBackToHeartbeatWhenProcessesUnknown(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"), Options{
		Stale:   45 * time.Second,
		Machine: "box",
		Alive:   func([]int) (map[int]bool, error) { return nil, errors.New("no snapshot") },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	s.now = func() time.Time { return now }
	reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 100})
	s.now = func() time.Time { return now.Add(time.Minute) }
	if list, _ := s.List(context.Background(), AgentFilter{}); len(list) != 0 {
		t.Fatalf("should be offline: %+v", list)
	}
}

// The "back online" feed line names the agent as it is now called.
func TestRevivalEventUsesCurrentName(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 1, SessionID: "s1"})
	s.now = func() time.Time { return now.Add(time.Minute) }
	// Automatic names aren't recycled for a day, so b asks for a's name
	// explicitly (a hand-over) to force a's revival under another name.
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 2, Name: a.Name})
	a2 := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 3, SessionID: "s1"}) // resumed
	if b.Name != a.Name || a2.Name == a.Name {
		t.Fatalf("setup: a=%s b=%s a2=%s", a.Name, b.Name, a2.Name)
	}
	feed, err := s.Feed(ctx, FeedReq{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range feed {
		if e.Kind == "joined" && e.AgentID == a.ID && e.Summary == a2.Name+" is back online" {
			if e.Agent != a2.Name {
				t.Fatalf("event agent %q, want %q", e.Agent, a2.Name)
			}
			return
		}
	}
	t.Fatalf("no revival event for %s in %+v", a2.Name, feed)
}

// Prune keeps the hub small without losing anything still useful: unread
// mail and its recipients stay, old feed lines and long-gone agents go.
func TestPruneDropsOldEventsAndLongGoneAgents(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	now := time.Now()
	s.now = func() time.Time { return now }
	gone := reg(t, s, RegisterReq{Harness: "codex", Project: "app", AnchorPID: 1})
	waiting := reg(t, s, RegisterReq{Harness: "cursor", Project: "app", AnchorPID: 2})
	sender := reg(t, s, RegisterReq{Harness: "gemini", Project: "app", AnchorPID: 3})
	if _, err := s.Send(ctx, SendReq{From: sender.ID, To: waiting.Name, Body: "read me when you're back"}); err != nil {
		t.Fatal(err)
	}
	_ = s.Leave(ctx, gone.ID)
	_ = s.Leave(ctx, waiting.ID)

	s.now = func() time.Time { return now.Add(8 * 24 * time.Hour) }
	if err := s.Prune(ctx, Retention); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, gone.ID); err == nil {
		t.Error("an agent offline past the retention window should be deleted")
	}
	if _, err := s.Get(ctx, waiting.ID); err != nil {
		t.Error("an agent with unread mail must be kept, or its mail is lost")
	}
	feed, _ := s.Feed(ctx, FeedReq{Limit: 100})
	if len(feed) != 0 {
		t.Errorf("old events should be pruned: %+v", feed)
	}
}
