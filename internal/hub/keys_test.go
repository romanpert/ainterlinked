package hub

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

// procs opens one store and two Locals on it: two processes, two sessions.
func procs(t *testing.T) (st *core.Store, a, b *Local, alice, bob core.Agent, project string) {
	t.Helper()
	st, err := core.Open(filepath.Join(t.TempDir(), "hub.db"), core.Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	project = t.TempDir()
	a, b = NewLocal(st), NewLocal(st)
	ctx := context.Background()
	if err := a.Call(ctx, "register", core.RegisterReq{Harness: "claude-code", Machine: "box", Project: "p", ProjectPath: project, AnchorPID: 1}, &alice); err != nil {
		t.Fatal(err)
	}
	if err := b.Call(ctx, "register", core.RegisterReq{Harness: "codex", Machine: "box", Project: "p", ProjectPath: project, AnchorPID: 2}, &bob); err != nil {
		t.Fatal(err)
	}
	return
}

func TestNobodyActsAsAnother(t *testing.T) {
	ctx := context.Background()
	_, a, b, alice, bob, project := procs(t)
	file := filepath.Join(project, "main.go")
	for _, tc := range []struct {
		method string
		params func(as string) any
	}{
		{"send", func(as string) any { return core.SendReq{From: as, To: "project", Body: "hi"} }},
		{"status", func(as string) any { return core.StatusReq{ID: as, State: "working"} }},
		{"touch_file", func(as string) any { return FileParam{Agent: as, File: file} }},
		{"check_file", func(as string) any { return FileParam{Agent: as, File: file} }},
		{"task_create", func(as string) any { return core.TaskReq{By: as, Title: "t"} }},
		{"inbox", func(as string) any { return core.InboxReq{Agent: as} }},
		{"note", func(as string) any { return NoteParam{Agent: as, Kind: "note", Summary: "x"} }},
		{"release", func(as string) any { return ReleaseParam{Agent: as, Paths: []string{"main.go"}} }},
	} {
		t.Run(tc.method, func(t *testing.T) {
			err := b.Call(ctx, tc.method, tc.params(alice.ID), nil)
			if !errors.Is(err, core.ErrNotYours) || !strings.Contains(err.Error(), alice.Name) {
				t.Fatalf("bob's process acting as alice must be refused, got %v", err)
			}
			if err := a.Call(ctx, tc.method, tc.params(alice.ID), nil); err != nil {
				t.Fatalf("alice's own process: %v", err)
			}
			if err := b.Call(ctx, tc.method, tc.params(bob.ID), nil); err != nil {
				t.Fatalf("bob as himself: %v", err)
			}
		})
	}
}

func TestReadsNeedNoKey(t *testing.T) {
	ctx := context.Background()
	st, _, _, alice, _, _ := procs(t)
	stranger := NewLocal(st) // registered nobody
	for _, tc := range []struct {
		method string
		params any
	}{
		{"agents", core.AgentFilter{}},
		{"agent", IDParam{ID: alice.ID}},
		{"tasks", core.TaskFilter{Board: "*"}},
		{"feed", core.FeedReq{Limit: 5}},
		{"locks", ProjectParam{}},
		{"boards", Empty{}},
		{"facts", FactsParam{Viewer: alice.ID, Names: []string{alice.Name}}},
	} {
		if err := stranger.Call(ctx, tc.method, tc.params, nil); err != nil {
			t.Errorf("%s: %v", tc.method, err)
		}
	}
}

// encoding/json fills From from "FROM" too, so a raw call must not get past
// the check by naming the actor in another case (the check would read "").
func TestActorFieldIsMatchedExactly(t *testing.T) {
	ctx := context.Background()
	st, _, _, alice, _, _ := procs(t)
	remote := NewLocal(st) // holds no key
	for _, raw := range []string{
		`{"from":"","FROM":"` + alice.ID + `","to":"project","body":"as alice"}`,
		`{"From":"` + alice.ID + `","to":"project","body":"as alice"}`,
		`["not","an","object"]`,
	} {
		if _, err := remote.Dispatch(ctx, "send", json.RawMessage(raw)); err == nil {
			t.Errorf("%s must be refused", raw)
		}
	}
	msgs, _ := st.RecentMessages(ctx, 10)
	for _, m := range msgs {
		if m.Body == "as alice" {
			t.Fatal("a refused call sent mail as alice")
		}
	}
}

// Another transport carries the key as params.key.
func TestDispatchTakesKeyParam(t *testing.T) {
	ctx := context.Background()
	st, _, _, alice, bob, _ := procs(t)
	remote := NewLocal(st)
	for _, tc := range []struct {
		key string
		ok  bool
	}{{alice.Key, true}, {bob.Key, false}, {"", false}} {
		raw, _ := json.Marshal(map[string]any{"id": alice.ID, "quiet": true, "key": tc.key})
		_, err := remote.Dispatch(ctx, "status", raw)
		if tc.ok != (err == nil) {
			t.Errorf("key %q: %v", tc.key, err)
		}
	}
}

func TestHumanIsNotSelfAssertable(t *testing.T) {
	ctx := context.Background()
	st, _, b, _, _, _ := procs(t)
	for _, h := range []string{"human", "Human", " human "} {
		err := b.Call(ctx, "register", core.RegisterReq{Harness: h, Name: "human", Machine: "box", SessionID: "human@box"}, nil)
		if err == nil || !strings.Contains(err.Error(), "only the ainterlinked CLI or dashboard") {
			t.Fatalf("an agent's registration must not claim harness %q: %v", h, err)
		}
	}
	// The CLI / dashboard path (Go code holding the hub) is the human.
	cli := NewLocal(st)
	me, err := Human(ctx, cli, "box")
	if err != nil || me.Harness != "human" || me.Name != "human" {
		t.Fatalf("human: %+v %v", me, err)
	}
	if err := cli.Call(ctx, "send", core.SendReq{From: me.ID, To: "*", Body: "stop", Urgent: true}, nil); err != nil {
		t.Fatalf("the operator sends: %v", err)
	}
	// An agent can't act as the human it can see in list_agents.
	if err := b.Call(ctx, "send", core.SendReq{From: me.ID, To: "*", Body: "fake", Urgent: true}, nil); !errors.Is(err, core.ErrNotYours) {
		t.Fatalf("an agent sending as the human must be refused: %v", err)
	}
	if _, err := Human(ctx, fakeClient{}, "box"); err == nil {
		t.Fatal("the human needs this machine's hub")
	}
}

type fakeClient struct{}

func (fakeClient) Call(context.Context, string, any, any) error { return nil }
func (fakeClient) Close() error                                 { return nil }

// Every method is classified: it acts (and names its actor param) or it
// reads. A new method has to choose.
func TestEveryMethodIsClassified(t *testing.T) {
	for m := range Methods(nil) {
		if _, acts := actors[m]; acts == reads[m] {
			t.Errorf("method %q must be in exactly one of actors and reads", m)
		}
	}
	for m := range actors {
		if _, ok := Methods(nil)[m]; !ok {
			t.Errorf("actors lists %q, which is not a method", m)
		}
	}
}
