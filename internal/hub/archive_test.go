package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/core"
)

// The hub archives closed tasks by itself only once the user sets
// [tasks] archive_after_days, and follows the setting live.
func TestTheHubArchivesOnlyWhenTheUserSaysSo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	if config.Default().Tasks.ArchiveAfterDays != 0 {
		t.Fatal("off by default")
	}
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Hour, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(st)
	ctx := context.Background()
	me, err := NewLocal(st).Human(ctx, "box")
	if err != nil {
		t.Fatal(err)
	}
	task, err := st.CreateTask(ctx, core.TaskReq{By: me.ID, Board: "shop", Title: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateTask(ctx, core.TaskUpdate{ID: task.ID, By: me.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(30 * 24 * time.Hour)
	onBoard := func() int {
		list, err := st.ListTasks(ctx, core.TaskFilter{Board: "*"})
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}

	stamp := srv.followLimits("")
	srv.archiveTick(ctx)
	if onBoard() != 1 {
		t.Fatal("no config: nothing archived")
	}
	if err := os.WriteFile(config.Path(), []byte("[tasks]\narchive_after_days = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp = srv.followLimits(stamp)
	srv.archiveTick(ctx)
	if onBoard() != 1 {
		t.Fatal("0: nothing archived")
	}
	// Another size: Linux keeps mtimes to a few ms, and the hub sees a change
	// by size and mtime.
	if err := os.WriteFile(config.Path(), []byte("[tasks]\narchive_after_days = 7 # a week\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.followLimits(stamp)
	srv.archiveTick(ctx)
	if onBoard() != 0 {
		t.Fatal("set: the old closed task is archived")
	}
	md, err := os.ReadFile(filepath.Join(ArchiveDir(), "shop.md"))
	if err != nil || !strings.Contains(string(md), "## #1 old (done)") {
		t.Fatalf("archive file: %v\n%s", err, md)
	}
}

// A board whose archive file can't be written doesn't hold the others back,
// and the hub says so in its log, once per tick, instead of dropping it.
func TestTheHubLogsABoardItCantArchive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", home)
	clock := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	st, err := core.Open(filepath.Join(home, "hub.db"), core.Options{Stale: time.Hour, Now: func() time.Time { return clock }})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := NewServer(st)
	ctx := context.Background()
	me, err := NewLocal(st).Human(ctx, "box")
	if err != nil {
		t.Fatal(err)
	}
	for _, board := range []string{"shop", "web"} {
		task, err := st.CreateTask(ctx, core.TaskReq{By: me.ID, Board: board, Title: "old on " + board})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.UpdateTask(ctx, core.TaskUpdate{ID: task.ID, By: me.ID, Status: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	clock = clock.Add(30 * 24 * time.Hour)
	bad := filepath.Join(ArchiveDir(), "web.md")
	if err := os.MkdirAll(bad, 0o700); err != nil { // a folder can't be appended to
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[tasks]\narchive_after_days = 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv.followLimits("")
	srv.archiveTick(ctx)
	srv.archiveTick(ctx)
	list, err := st.ListTasks(ctx, core.TaskFilter{Board: "*"})
	if err != nil || len(list) != 1 || list[0].Board != "web" {
		t.Fatalf("shop archived, web kept: %+v %v", list, err)
	}
	md, _ := os.ReadFile(filepath.Join(ArchiveDir(), "shop.md"))
	if n := strings.Count(string(md), "## #1 "); n != 1 {
		t.Fatalf("shop's task written %d times:\n%s", n, md)
	}
	log, _ := os.ReadFile(filepath.Join(home, "hub.log"))
	n := 0
	for _, line := range strings.Split(string(log), "\n") {
		if strings.Contains(line, bad) {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("one log line per tick naming the file, got %d:\n%s", n, log)
	}
}

// Agents read boards and search; archiving goes through the human only.
func TestTaskArchiveIsTheHumans(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	st, err := core.Open(filepath.Join(t.TempDir(), "hub.db"), core.Options{Stale: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	l := NewLocal(st)
	ctx := context.Background()
	var a core.Agent
	if err := l.Call(ctx, "register", core.RegisterReq{Harness: "codex", Project: "shop", Machine: "box", AnchorPID: 3}, &a); err != nil {
		t.Fatal(err)
	}
	var res core.ArchiveResult
	if err := l.Call(ctx, "task_archive", core.ArchiveReq{By: a.ID}, &res); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("an agent can't archive: %v", err)
	}
	me, err := l.Human(ctx, "box")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Call(ctx, "task_archive", core.ArchiveReq{By: me.ID}, &res); err != nil || res.Count != 0 {
		t.Fatalf("the human can: %+v %v", res, err)
	}
	var page core.TaskPage
	if err := l.Call(ctx, "task_list", core.TaskFilter{Board: "*", Query: "anything"}, &page); err != nil {
		t.Fatal(err)
	}
}
