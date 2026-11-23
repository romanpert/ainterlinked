package core

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// board is a store with the human, one agent, and a clock the test moves.
type board struct {
	*Store
	t      *testing.T
	ctx    context.Context
	clock  time.Time
	human  Agent
	worker Agent
	dir    string
}

func newBoard(t *testing.T) *board {
	t.Helper()
	b := &board{Store: newStore(t), t: t, ctx: context.Background(), clock: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC), dir: t.TempDir()}
	b.now = func() time.Time { return b.clock }
	b.human = reg(t, b.Store, RegisterReq{Harness: "human", Name: "human", Project: "shop", AnchorPID: 9})
	b.worker = reg(t, b.Store, RegisterReq{Harness: "claude-code", Project: "shop", AnchorPID: 1})
	return b
}

func (b *board) add(title, body string, labels ...string) Task {
	b.t.Helper()
	t, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: "shop", Title: title, Body: body, Labels: labels})
	if err != nil {
		b.t.Fatal(err)
	}
	return t
}

// finish has the worker claim the task and close it with status.
func (b *board) finish(id int64, status, result string) {
	b.t.Helper()
	if _, err := b.Claim(b.ctx, b.worker.ID, id); err != nil {
		b.t.Fatal(err)
	}
	if _, err := b.UpdateTask(b.ctx, TaskUpdate{ID: id, By: b.worker.ID, Status: status, Result: result}); err != nil {
		b.t.Fatal(err)
	}
}

func (b *board) archive(r ArchiveReq) ArchiveResult {
	b.t.Helper()
	r.By, r.Dir = b.human.ID, b.dir
	res, err := b.ArchiveTasks(b.ctx, r)
	if err != nil {
		b.t.Fatal(err)
	}
	return res
}

func (b *board) count(table string) int {
	b.t.Helper()
	var n int
	if err := b.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		b.t.Fatal(err)
	}
	return n
}

func TestABoardShowsOpenTasksAndCountsTheClosedOnes(t *testing.T) {
	b := newBoard(t)
	open := b.add("still to do", "")
	b.finish(b.add("shipped", "").ID, "done", "ok")
	b.finish(b.add("dropped", "").ID, "cancelled", "")
	for _, f := range []TaskFilter{{Board: "shop"}, {Board: "shop", Open: true}} {
		p, err := b.TaskList(b.ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.Tasks) != 1 || p.Tasks[0].ID != open.ID || p.Closed != 2 {
			t.Fatalf("%+v: open tasks and a count of the closed: %+v", f, p)
		}
	}
	p, err := b.TaskList(b.ctx, TaskFilter{Board: "shop", Status: "done"})
	if err != nil || len(p.Tasks) != 1 || p.Tasks[0].Title != "shipped" || p.Closed != 0 {
		t.Fatalf("asking for done lists them: %+v %v", p, err)
	}
	if _, err := b.TaskList(b.ctx, TaskFilter{Board: "shop", Status: "closed"}); err == nil {
		t.Fatal("an unknown status is refused, not an empty board")
	}
	// The agent's default board is its project's.
	p, _ = b.TaskList(b.ctx, TaskFilter{Agent: b.worker.ID})
	if len(p.Tasks) != 1 || p.Closed != 2 {
		t.Fatalf("default board: %+v", p)
	}
}

func TestArchiveWritesMarkdownThenMovesTheRows(t *testing.T) {
	b := newBoard(t)
	keep := b.add("open work", "")
	done := b.add("Fix login", "The form posts twice.", "auth", "ui")
	b.finish(done.ID, "done", "debounced the button")
	gone := b.add("Old idea", "")
	b.finish(gone.ID, "cancelled", "")

	res := b.archive(ArchiveReq{})
	file := filepath.Join(b.dir, "shop.md")
	if res.Count != 2 || len(res.Files) != 1 || res.Files[0].Path != file || !reflect.DeepEqual(res.Files[0].IDs, []int64{done.ID, gone.ID}) {
		t.Fatalf("result: %+v", res)
	}
	md, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Archived tasks", "## #2 Fix login (done)", "board: shop", "labels: auth, ui", "assignee: " + b.worker.Name,
		"creator: human", "created 2026-11-01", "The form posts twice.", "Result: debounced the button", "## #3 Old idea (cancelled)"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("archive misses %q:\n%s", want, md)
		}
	}
	if b.count("tasks") != 1 || b.count("archived_tasks") != 2 {
		t.Fatalf("rows: %d on the board, %d archived", b.count("tasks"), b.count("archived_tasks"))
	}
	boards, _ := b.Boards(b.ctx)
	if len(boards) != 1 || boards[0].Total != 1 {
		t.Fatalf("archived tasks leave the board's counts: %+v", boards)
	}
	p, _ := b.TaskList(b.ctx, TaskFilter{Board: "shop"})
	if len(p.Tasks) != 1 || p.Tasks[0].ID != keep.ID || p.Closed != 0 {
		t.Fatalf("board after: %+v", p)
	}
	// A second run appends to the same file.
	b.finish(keep.ID, "done", "")
	b.archive(ArchiveReq{})
	md, _ = os.ReadFile(file)
	if strings.Count(string(md), "# Archived tasks") != 1 || !strings.Contains(string(md), "## #1 open work (done)") {
		t.Fatalf("second run appends:\n%s", md)
	}
}

// The file is written first: when it can't be, nothing leaves the board.
func TestArchiveMovesNothingWhenTheFileCantBeWritten(t *testing.T) {
	b := newBoard(t)
	b.finish(b.add("done", "").ID, "done", "")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := b.ArchiveTasks(b.ctx, ArchiveReq{By: b.human.ID, To: filepath.Join(blocker, "sub", "a.md")})
	if err == nil {
		t.Fatal("an unwritable archive is an error")
	}
	if b.count("tasks") != 1 || b.count("archived_tasks") != 0 {
		t.Fatal("no file, no move")
	}
}

func TestOnlyTheHumanArchives(t *testing.T) {
	b := newBoard(t)
	b.finish(b.add("done", "").ID, "done", "")
	if _, err := b.ArchiveTasks(b.ctx, ArchiveReq{By: b.worker.ID, Dir: b.dir}); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("an agent can't archive: %v", err)
	}
	if b.count("archived_tasks") != 0 {
		t.Fatal("nothing archived")
	}
}

func TestArchiveOlderThanAndOneBoard(t *testing.T) {
	b := newBoard(t)
	old := b.add("old", "")
	b.finish(old.ID, "done", "")
	b.clock = b.clock.Add(6 * 24 * time.Hour)
	recent := b.add("recent", "")
	b.finish(recent.ID, "done", "")
	other, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: "web", Title: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}
	b.finish(other.ID, "cancelled", "")
	b.clock = b.clock.Add(2 * 24 * time.Hour)

	res := b.archive(ArchiveReq{Board: "shop", OlderThan: (7 * 24 * time.Hour).Milliseconds()})
	if res.Count != 1 || res.Files[0].IDs[0] != old.ID {
		t.Fatalf("only shop's task closed over 7 days ago: %+v", res)
	}
	res = b.archive(ArchiveReq{To: filepath.Join(b.dir, "all.md")})
	if res.Count != 2 || len(res.Files) != 1 || !strings.HasSuffix(res.Files[0].Path, "all.md") {
		t.Fatalf("the rest, every board, into one file: %+v", res)
	}
}

// [tasks] archive_after_days is off by default (the hub calls AutoArchive
// only when it is set); set, it archives what was closed that long ago.
func TestAutoArchive(t *testing.T) {
	b := newBoard(t)
	old := b.add("old", "")
	b.finish(old.ID, "done", "")
	b.clock = b.clock.Add(4 * 24 * time.Hour)
	b.finish(b.add("new", "").ID, "done", "")
	if res, err := b.AutoArchive(b.ctx, 0, b.dir); err != nil || res.Count != 0 {
		t.Fatalf("0 archives nothing: %+v %v", res, err)
	}
	res, err := b.AutoArchive(b.ctx, 3*24*time.Hour, b.dir)
	if err != nil || res.Count != 1 || res.Files[0].IDs[0] != old.ID {
		t.Fatalf("auto archive: %+v %v", res, err)
	}
	if _, err := os.Stat(filepath.Join(b.dir, "shop.md")); err != nil {
		t.Fatal(err)
	}
}

func TestAnArchivedDependencyIsFinished(t *testing.T) {
	b := newBoard(t)
	first := b.add("first", "")
	then, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: "shop", Title: "then", DependsOn: []int64{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	b.finish(first.ID, "done", "")
	b.archive(ArchiveReq{})
	list, _ := b.ListTasks(b.ctx, TaskFilter{Board: "shop"})
	if len(list) != 1 || list[0].Blocked {
		t.Fatalf("waits on nothing: %+v", list)
	}
	if _, err := b.Claim(b.ctx, b.worker.ID, then.ID); err != nil {
		t.Fatalf("claimable: %v", err)
	}
	if _, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: "shop", Title: "later", DependsOn: []int64{first.ID}}); err != nil {
		t.Fatalf("an archived task can be depended on: %v", err)
	}
	if _, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: "shop", Title: "x", DependsOn: []int64{999}}); err == nil {
		t.Fatal("a task that never was can't")
	}
}

func TestArchiveDropsWantsAndQueuePlaces(t *testing.T) {
	b := newBoard(t)
	done := b.add("done", "")
	b.finish(done.ID, "done", "")
	// Left over from a binary that didn't clear them at close.
	if _, err := b.db.Exec(`INSERT INTO task_wants(task_id,agent_id,created_at) VALUES(?,?,1)`, done.ID, b.human.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := b.db.Exec(`INSERT INTO waiters(project_path,pattern,agent_id,task_id,ttl_ms,created_at,expires_at) VALUES('/w','a.go',?,?,1,1,9e15)`, b.worker.ID, done.ID); err != nil {
		t.Fatal(err)
	}
	b.archive(ArchiveReq{})
	if b.count("task_wants") != 0 || b.count("waiters") != 0 {
		t.Fatal("wants and queue places of archived tasks go")
	}
}

// Reservations linked to a task are let go when it is closed, before it
// can be archived.
func TestAClosedTaskHoldsNoReservations(t *testing.T) {
	b := newBoard(t)
	task := b.add("edit", "")
	if _, err := b.Reserve(b.ctx, LockReq{Agent: b.worker.ID, Paths: []string{"a.go"}, Task: task.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.UpdateTask(b.ctx, TaskUpdate{ID: task.ID, By: b.worker.ID, Status: "done"}); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = b.db.QueryRow(`SELECT count(*) FROM locks WHERE task_id=?`, task.ID).Scan(&n)
	if n != 0 {
		t.Fatal("closing released them")
	}
}

func TestArchivingDoesntChangeAnAgentsFacts(t *testing.T) {
	b := newBoard(t)
	b.finish(b.add("one", "").ID, "done", "")
	b.finish(b.add("two", "").ID, "cancelled", "")
	if _, err := b.Claim(b.ctx, b.worker.ID, b.add("three", "").ID); err != nil {
		t.Fatal(err)
	}
	facts := func() Facts {
		f, err := b.AgentFacts(b.ctx, "", []string{b.worker.Name})
		if err != nil {
			t.Fatal(err)
		}
		return f[strings.ToLower(b.worker.Name)]
	}
	before := facts()
	if before.Taken != 3 || before.Done != 1 || before.Open != 1 {
		t.Fatalf("before: %+v", before)
	}
	b.archive(ArchiveReq{})
	if after := facts(); !reflect.DeepEqual(after, before) {
		t.Fatalf("archiving changed facts: %+v → %+v", before, after)
	}
}

func TestAnArchivedTaskSaysSoByID(t *testing.T) {
	b := newBoard(t)
	task := b.add("done", "")
	b.finish(task.ID, "done", "")
	b.archive(ArchiveReq{})
	for name, err := range map[string]error{
		"update": func() error {
			_, err := b.UpdateTask(b.ctx, TaskUpdate{ID: task.ID, By: b.human.ID, Status: "todo"})
			return err
		}(),
		"claim": func() error { _, err := b.Claim(b.ctx, b.worker.ID, task.ID); return err }(),
		"want":  func() error { _, err := b.WantTask(b.ctx, b.worker.ID, task.ID); return err }(),
	} {
		if err == nil || !strings.Contains(err.Error(), "is archived") {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, err := b.Claim(b.ctx, b.worker.ID, 999); err == nil || err.Error() != "no task #999" {
		t.Fatalf("a task that never was: %v", err)
	}
	p, err := b.TaskList(b.ctx, TaskFilter{Query: "#1"})
	if err != nil || len(p.Tasks) != 1 || p.Tasks[0].ArchivedAt == 0 {
		t.Fatalf("#1 reads it: %+v %v", p, err)
	}
}

func TestContinueAsCarriesArchivedTasks(t *testing.T) {
	b := newBoard(t)
	b.finish(b.add("one", "").ID, "done", "")
	b.archive(ArchiveReq{})
	if err := b.Leave(b.ctx, b.worker.ID); err != nil {
		t.Fatal(err)
	}
	b.clock = b.clock.Add(time.Hour)
	next := reg(t, b.Store, RegisterReq{Harness: "codex", Project: "shop", AnchorPID: 2})
	if _, err := b.ContinueAs(b.ctx, next.ID, b.worker.Name); err != nil {
		t.Fatal(err)
	}
	var id string
	_ = b.db.QueryRow(`SELECT assignee_id FROM archived_tasks`).Scan(&id)
	if id != next.ID {
		t.Fatalf("archived work follows the lineage: %q", id)
	}
}

// Each file is written and its rows moved on their own: a file that can't
// be written keeps its own board's tasks, not the others', and the next run
// doesn't append the others' again.
func TestArchiveAFailingFileBlocksOnlyItsBoard(t *testing.T) {
	b := newBoard(t)
	ids := map[string]int64{}
	for _, board := range []string{"aaa", "zzz"} {
		task, err := b.CreateTask(b.ctx, TaskReq{By: b.human.ID, Board: board, Title: "closed on " + board})
		if err != nil {
			t.Fatal(err)
		}
		b.finish(task.ID, "done", "")
		ids[board] = task.ID
	}
	bad := filepath.Join(b.dir, "zzz.md")
	if err := os.Mkdir(bad, 0o700); err != nil { // a folder can't be appended to
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		res, err := b.ArchiveTasks(b.ctx, ArchiveReq{By: b.human.ID, Dir: b.dir})
		if len(res.Failed) != 1 || res.Failed[0].Path != bad || res.Failed[0].Error == "" || res.Failed[0].IDs[0] != ids["zzz"] {
			t.Fatalf("run %d: the failing file is reported: %+v", run, res)
		}
		if run == 1 && (err != nil || res.Count != 1 || len(res.Files) != 1 || res.Files[0].IDs[0] != ids["aaa"]) {
			t.Fatalf("run 1: aaa archived, no error: %+v %v", res, err)
		}
		// Nothing archived and a file failing is an error.
		if run == 2 && (res.Count != 0 || err == nil || !strings.Contains(err.Error(), "zzz.md")) {
			t.Fatalf("run 2: nothing more, an error naming the file: %+v %v", res, err)
		}
	}
	if b.count("tasks") != 1 || b.count("archived_tasks") != 1 {
		t.Fatalf("rows: %d on a board, %d archived", b.count("tasks"), b.count("archived_tasks"))
	}
	md, _ := os.ReadFile(filepath.Join(b.dir, "aaa.md"))
	if n := strings.Count(string(md), fmt.Sprintf("## #%d ", ids["aaa"])); n != 1 {
		t.Fatalf("aaa's task written %d times:\n%s", n, md)
	}
}

// A crash after a file is written and before its rows move leaves the
// tasks on the board; the next run doesn't write them twice.
func TestArchiveSkipsTasksAlreadyInTheFile(t *testing.T) {
	b := newBoard(t)
	written := b.add("written before a crash", "")
	b.finish(written.ID, "done", "")
	fresh := b.add("not yet written", "")
	b.finish(fresh.ID, "cancelled", "")
	closed, err := b.ListTasks(b.ctx, TaskFilter{Board: "shop", Status: "done"})
	if err != nil || len(closed) != 1 {
		t.Fatalf("%+v %v", closed, err)
	}
	file := filepath.Join(b.dir, "shop.md")
	if err := appendArchive(file, closed, b.ms()); err != nil {
		t.Fatal(err)
	}
	res := b.archive(ArchiveReq{})
	if res.Count != 2 || b.count("archived_tasks") != 2 {
		t.Fatalf("both move: %+v", res)
	}
	md, _ := os.ReadFile(file)
	for id, want := range map[int64]int{written.ID: 1, fresh.ID: 1} {
		if n := strings.Count(string(md), fmt.Sprintf("## #%d ", id)); n != want {
			t.Fatalf("#%d written %d times:\n%s", id, n, md)
		}
	}
}

func TestFileSafe(t *testing.T) {
	for in, want := range map[string]string{"shop": "shop", "my app/v2": "my-app-v2", "..": "default", "": "default", "a:b": "a-b",
		// Windows device names, on every OS so a board has one file name everywhere.
		"con": "_con", "CON": "_CON", "prn": "_prn", "Aux": "_Aux", "nul": "_nul", "com1": "_com1", "COM9": "_COM9", "lpt1": "_lpt1",
		"lpt9": "_lpt9", "nul.txt": "_nul.txt", "con.tar.gz": "_con.tar.gz", "prn ": "_prn", "com0": "_com0",
		"console": "console", "com10": "com10", "lpt": "lpt", "aux-x": "aux-x", "nully": "nully"} {
		if got := fileSafe(in); got != want {
			t.Errorf("fileSafe(%q) = %q, want %q", in, got, want)
		}
	}
}
