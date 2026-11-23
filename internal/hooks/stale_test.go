package hooks

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// The briefing counts forgotten tasks only when the user set a number of
// days, and never a task in progress (abandon looks after those).
func TestForgottenCountsOnlyWhenAsked(t *testing.T) {
	tasks := []core.Task{
		{Status: "todo", Untouched: 20}, {Status: "review", Untouched: 14}, {Status: "blocked", Untouched: 13},
		{Status: "doing", Untouched: 30},
	}
	if n := forgotten(tasks, 0); n != 0 {
		t.Fatalf("off: %d", n)
	}
	if n := forgotten(tasks, 14); n != 2 {
		t.Fatalf("todo 20d and review 14d: %d", n)
	}
}

func TestBoardLineAddsTheCountOnlyWhenSet(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	now := time.Now()
	st, err := core.Open(filepath.Join(t.TempDir(), "hub.db"), core.Options{Stale: time.Minute, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	me, err := st.Register(ctx, core.RegisterReq{Harness: "codex", Project: "shop", ProjectPath: "/w/shop", Machine: "box", AnchorPID: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"a", "b"} {
		if _, err := st.CreateTask(ctx, core.TaskReq{By: me.ID, Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(15 * 24 * time.Hour)
	c := hub.NewLocal(st)
	if got := boardLine(ctx, c, me, 0, 0); !strings.HasPrefix(got, "\nBoard \"shop\": 2 open.") {
		t.Fatalf("off: %q", got)
	}
	if got := boardLine(ctx, c, me, 0, 14); !strings.HasPrefix(got, "\nBoard \"shop\": 2 open, 2 untouched 14d+.") {
		t.Fatalf("on: %q", got)
	}
}
