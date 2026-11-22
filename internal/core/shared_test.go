package core

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A shared file is a ledger many agents keep at once (an open-items list, a
// decision log): the owner declares it so in the repo's covenant and nobody
// holds it. Seen in real use (2026-11-22): 221 of 249 queue waits in one day
// were for three such files, each held whole for a one-line edit.
func sharedStore(t *testing.T, shared ...string) *Store {
	t.Helper()
	set := map[string]bool{}
	for _, f := range shared {
		set[f] = true
	}
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"), Options{Stale: 45 * time.Second,
		Shared: func(project, rel string) bool { return project == "/work/app" && set[rel] }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEditingASharedFileReservesNothing(t *testing.T) {
	s := sharedStore(t, "NEXT.md")
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 2})
	if l, _ := s.TouchFile(ctx, a.ID, "NEXT.md"); l != nil {
		t.Fatalf("a: %+v", l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "NEXT.md"); l != nil {
		t.Fatalf("b was blocked on a shared file by %+v", l)
	}
	if ls, _ := s.Locks(ctx, "/work/app"); len(ls) != 0 {
		t.Fatalf("editing a shared file reserved it: %+v", ls)
	}
	// Not shared: editing still reserves, as before.
	if l, _ := s.TouchFile(ctx, a.ID, "src/app.go"); l != nil {
		t.Fatal(l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "src/app.go"); l == nil || l.AgentID != a.ID {
		t.Fatalf("an ordinary file must stay a's: %+v", l)
	}
}

func TestNobodyHoldsASharedFile(t *testing.T) {
	s := sharedStore(t, "NEXT.md")
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 2})
	// Asking for it by name is refused with the reason, and queues nothing.
	res, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"NEXT.md", "src/"}, Wait: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Granted) != 1 || res.Granted[0].Pattern != "src/**" || len(res.Queued) != 0 ||
		len(res.Conflicts) != 1 || res.Conflicts[0].Agent != SystemSender {
		t.Fatalf("want src/ granted and NEXT.md refused by the covenant: %+v", res)
	}
	// A wider reservation that covers it doesn't hold it either.
	if _, err := s.Reserve(ctx, LockReq{Agent: a.ID, Paths: []string{"**"}}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "NEXT.md"); l != nil {
		t.Fatalf("a's ** held a shared file: %+v", l)
	}
	if l, _ := s.CheckFile(ctx, b.ID, "NEXT.md"); l != nil {
		t.Fatalf("check_file reports a shared file as held: %+v", l)
	}
	if l, _ := s.CheckFile(ctx, b.ID, "src/app.go"); l == nil {
		t.Fatal("the rest of a's ** must still be a's")
	}
}

// Only the owner takes files out of reservations; the hub keeps the list.
func TestOnlyTheOwnerSharesFiles(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 2})
	owner := reg(t, s, RegisterReq{Harness: "human", Project: "app", AnchorPID: 3})
	if _, err := s.SetShared(ctx, SharedReq{By: a.ID, ProjectPath: "/work/app", Files: []string{"src/held.go"}}); err == nil {
		t.Fatal("an agent shared a file")
	}
	for _, bad := range []string{"**", "src/*.go", "docs/", "."} {
		if _, err := s.SetShared(ctx, SharedReq{By: owner.ID, ProjectPath: "/work/app", Files: []string{bad}}); err == nil {
			t.Fatalf("%q was accepted as a shared file", bad)
		}
	}
	got, err := s.SetShared(ctx, SharedReq{By: owner.ID, ProjectPath: "/work/app", Files: []string{"./NEXT.md", "/work/app/docs/log.md", "NEXT.md"}})
	if err != nil || strings.Join(got.Files, ",") != "NEXT.md,docs/log.md" {
		t.Fatalf("%+v %v", got, err)
	}
	if l, _ := s.TouchFile(ctx, a.ID, "NEXT.md"); l != nil {
		t.Fatal(l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "/work/app/NEXT.md"); l != nil {
		t.Fatalf("a shared file was held: %+v", l)
	}
	if _, err := s.SetShared(ctx, SharedReq{By: owner.ID, ProjectPath: "/work/app"}); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.TouchFile(ctx, a.ID, "NEXT.md"); l != nil {
		t.Fatal(l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "NEXT.md"); l == nil {
		t.Fatal("unshared, NEXT.md is the editor's again")
	}
	if list, _ := s.GetShared(ctx, "/work/app"); len(list.Files) != 0 {
		t.Fatalf("cleared list: %+v", list)
	}
}

func TestReservationsMadeBeforeTheDeclarationStopHolding(t *testing.T) {
	shared := false
	s, err := Open(filepath.Join(t.TempDir(), "hub.db"), Options{Stale: 45 * time.Second,
		Shared: func(_, rel string) bool { return shared && rel == "NEXT.md" }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 1})
	b := reg(t, s, RegisterReq{Harness: "claude-code", Project: "app", AnchorPID: 2})
	if l, _ := s.TouchFile(ctx, a.ID, "NEXT.md"); l != nil {
		t.Fatal(l)
	}
	if l, _ := s.TouchFile(ctx, b.ID, "NEXT.md"); l == nil {
		t.Fatal("before the declaration NEXT.md is a's")
	}
	shared = true // the owner commits the covenant
	if l, _ := s.TouchFile(ctx, b.ID, "NEXT.md"); l != nil {
		t.Fatalf("a's earlier lease still blocks: %+v", l)
	}
}
