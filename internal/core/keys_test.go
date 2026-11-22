package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentKeys(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	alice := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1})
	bob := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 2})
	if alice.Key == "" || bob.Key == "" || alice.Key == bob.Key {
		t.Fatalf("each agent needs its own key: %q %q", alice.Key, bob.Key)
	}
	// The same session registering again (another hook process) gets the same key.
	if again := reg(t, s, RegisterReq{Harness: "claude-code", Project: "api", AnchorPID: 1}); again.ID != alice.ID || again.Key != alice.Key {
		t.Fatalf("re-registration must give the same key: %+v", again)
	}
	// Only a hash is stored.
	var stored string
	if err := s.db.QueryRow(`SELECT key_hash FROM agents WHERE id=?`, alice.ID).Scan(&stored); err != nil || stored != hashKey(alice.Key) || strings.Contains(stored, alice.Key) {
		t.Fatalf("stored %q, err %v", stored, err)
	}

	for _, tc := range []struct {
		name, as, key string
		ok            bool
	}{
		{"own key", alice.ID, alice.Key, true},
		{"no key", alice.ID, "", false},
		{"another agent's key", alice.ID, bob.Key, false},
		{"made-up key", alice.ID, "x", false},
		{"unknown agent: the method reports it", "nobody", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Authorize(ctx, tc.as, tc.key)
			if tc.ok != (err == nil) {
				t.Fatalf("Authorize = %v, want ok=%v", err, tc.ok)
			}
			if err != nil && (!errors.Is(err, ErrNotYours) || !strings.Contains(err.Error(), alice.Name) || !strings.Contains(err.Error(), "act as yourself")) {
				t.Fatalf("the error must name the agent and say what to do: %v", err)
			}
		})
	}
}

func TestLegacyAgentWithoutKeyHash(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	a := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 3})
	// As an older version left it: no key_hash.
	if _, err := s.db.Exec(`UPDATE agents SET key_hash='' WHERE id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Authorize(ctx, a.ID, ""); !errors.Is(err, ErrNotYours) {
		t.Fatalf("an agent with no key yet can't be acted as by a process that never registered it: %v", err)
	}
	// Its own session's next registration (any hook, an MCP restart) issues its key.
	again := reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 3})
	if again.ID != a.ID {
		t.Fatalf("the session resumes its agent: %s vs %s", again.ID, a.ID)
	}
	if err := s.Authorize(ctx, a.ID, ""); !errors.Is(err, ErrNotYours) {
		t.Fatalf("after registering with this version its key is required: %v", err)
	}
	if err := s.Authorize(ctx, a.ID, again.Key); err != nil {
		t.Fatal(err)
	}
}

func TestHubSecretSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	open := func() *Store {
		s, err := Open(path, Options{Stale: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	s := open()
	first := reg(t, s, RegisterReq{Harness: "codex", Project: "api", SessionID: "s1"})
	s.Close()
	if st, err := os.Stat(filepath.Join(filepath.Dir(path), "hub.key")); err != nil || st.Size() != secretLen {
		t.Fatalf("hub.key: %v %v", st, err)
	}
	s = open()
	defer s.Close()
	if second := reg(t, s, RegisterReq{Harness: "codex", Project: "api", SessionID: "s1"}); second.Key != first.Key {
		t.Fatal("a later process must derive the same key from the same secret")
	}
}

func TestKeyNeverListed(t *testing.T) {
	s := newStore(t)
	reg(t, s, RegisterReq{Harness: "codex", Project: "api", AnchorPID: 4})
	list, err := s.List(context.Background(), AgentFilter{})
	if err != nil || len(list) != 1 || list[0].Key != "" {
		t.Fatalf("only Register answers with a key: %+v %v", list, err)
	}
}
