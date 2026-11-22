package core

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// A session id is not who you are: it can be read (in a transcript's file
// name, in a hook payload). Registering with another agent's session id
// from another live process makes a new agent, not that one; once that
// agent's harness has exited, the same id resumes it (a restart, a resumed
// session).
func TestASessionIDDoesntTakeOverALiveAgent(t *testing.T) {
	running := map[int]bool{100: true, 200: true}
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"), Options{
		Stale:   45 * time.Second,
		Machine: "box",
		Alive: func(pids []int) (map[int]bool, error) {
			out := map[int]bool{}
			for _, p := range pids {
				out[p] = running[p]
			}
			return out, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	victim := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 100, SessionID: "sess-v"})
	thief := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 200, SessionID: "sess-v"})
	if thief.ID == victim.ID {
		t.Fatal("another live process took the agent over with its session id")
	}
	if a, _ := s.Get(ctx, victim.ID); a.AnchorPID != 100 {
		t.Fatalf("the victim's anchor moved to %d", a.AnchorPID)
	}
	// The victim's harness exits; resuming its session from a new process
	// picks it up again.
	delete(running, 100)
	back := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 300, SessionID: "sess-v"})
	if back.ID != victim.ID {
		t.Fatalf("a resumed session didn't find its agent: %s vs %s", back.ID, victim.ID)
	}
}
