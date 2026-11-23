package core

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func (b *board) search(f TaskFilter) TaskPage {
	b.t.Helper()
	if f.Board == "" {
		f.Board = "*"
	}
	p, err := b.TaskList(b.ctx, f)
	if err != nil {
		b.t.Fatal(err)
	}
	return p
}

func titles(p TaskPage) []string {
	out := make([]string, len(p.Tasks))
	for i, t := range p.Tasks {
		out[i] = t.Title
	}
	return out
}

func TestSearchFindsSubstringsAnyCaseAndAccent(t *testing.T) {
	b := newBoard(t)
	b.add("Reservas de sala", "")
	b.add("Reservación de mesas", "")
	b.add("Señales del router", "")
	b.add("unrelated", "")
	// Two accents in a word, or one in a short word, are more than a typo:
	// only folding finds these.
	b.add("Cañón del Colca", "")
	b.add("Café con leche", "")
	for q, want := range map[string]string{
		"canon":       "Cañón del Colca",
		"CAÑON":       "Cañón del Colca",
		"cafe":        "Café con leche",
		"reserv":      "Reservación de mesas,Reservas de sala",
		"RESERVACION": "Reservación de mesas",
		"reservación": "Reservación de mesas",
		"senal":       "Señales del router",
		"SEÑAL":       "Señales del router",
		"sala reserv": "Reservas de sala",
	} {
		if got := strings.Join(titles(b.search(TaskFilter{Query: q})), ","); got != want {
			t.Errorf("%q: got %q, want %q", q, got, want)
		}
	}
}

func TestSearchForgivesOneTypoInLongWords(t *testing.T) {
	b := newBoard(t)
	b.add("Migrate the reservations table", "")
	b.add("Fix the sala", "")
	for _, q := range []string{"resrvations", "reservatoins", "reservetions", "reservationss", "migrat reservatons"} {
		if got := titles(b.search(TaskFilter{Query: q})); len(got) != 1 || got[0] != "Migrate the reservations table" {
			t.Errorf("%q: %v", q, got)
		}
	}
	for _, q := range []string{"sapa", "resevrationz", "zzzzz"} { // short word, two typos, nothing
		if got := titles(b.search(TaskFilter{Query: q})); len(got) != 0 {
			t.Errorf("%q should find nothing: %v", q, got)
		}
	}
	// An exact match ranks above a near one.
	b.add("reservations", "")
	b.add("reservatioms", "")
	if got := titles(b.search(TaskFilter{Query: "reservatioms"})); len(got) != 3 || got[0] != "reservatioms" {
		t.Errorf("exact first: %v", got)
	}
}

func TestSearchReadsEveryTextField(t *testing.T) {
	b := newBoard(t)
	task := b.add("plain title", "the checkout page double-charges cards on retry", "payments")
	b.finish(task.ID, "done", "added an idempotency key")
	for _, q := range []string{"double-charges", "idempotency", "payments", b.worker.Name, "human"} {
		p := b.search(TaskFilter{Query: q})
		if len(p.Tasks) != 1 {
			t.Errorf("%q: %v", q, titles(p))
		}
	}
	p := b.search(TaskFilter{Query: "double"})
	if s := p.Tasks[0].Snippet; !strings.Contains(s, "double-charges") || strings.Contains(s, "\n") {
		t.Errorf("snippet: %q", s)
	}
	if s := b.search(TaskFilter{Query: "plain"}).Tasks[0].Snippet; s != "" {
		t.Errorf("a title match needs no snippet: %q", s)
	}
}

func TestSearchFindsArchivedTasksUnlessNarrowed(t *testing.T) {
	b := newBoard(t)
	old := b.add("Rotate the API keys", "")
	b.finish(old.ID, "done", "")
	b.archive(ArchiveReq{})
	b.add("Rotate the logs", "")
	p := b.search(TaskFilter{Query: "rotate"})
	if len(p.Tasks) != 2 || p.Tasks[0].Title != "Rotate the logs" || p.Tasks[1].ArchivedAt == 0 || p.Tasks[0].ArchivedAt != 0 {
		t.Fatalf("open first, archived marked: %+v", p.Tasks)
	}
	if got := titles(b.search(TaskFilter{Query: "rotate", Open: true})); len(got) != 1 || got[0] != "Rotate the logs" {
		t.Fatalf("open narrows: %v", got)
	}
	if got := titles(b.search(TaskFilter{Query: "rotate", Status: "done"})); len(got) != 1 || got[0] != "Rotate the API keys" {
		t.Fatalf("status narrows: %v", got)
	}
}

func TestSearchLimit(t *testing.T) {
	b := newBoard(t)
	for i := 0; i < 25; i++ {
		b.add(fmt.Sprintf("deploy step %d", i), "")
	}
	p := b.search(TaskFilter{Query: "deploy"})
	if len(p.Tasks) != searchLimit || p.More != 5 {
		t.Fatalf("default limit: %d shown, %d more", len(p.Tasks), p.More)
	}
	if p = b.search(TaskFilter{Query: "deploy", Limit: 24}); len(p.Tasks) != 24 || p.More != 1 {
		t.Fatalf("one past the limit: %d shown, %d more", len(p.Tasks), p.More)
	}
	p = b.search(TaskFilter{Query: "deploy", Limit: 3})
	if len(p.Tasks) != 3 || p.More != 22 || p.Tasks[0].Title != "deploy step 24" {
		t.Fatalf("limit 3, newest first: %v, %d more", titles(p), p.More)
	}
	p = b.search(TaskFilter{Query: "nothing like it"})
	if len(p.Tasks) != 0 || p.More != 0 {
		t.Fatalf("no match: %+v", p)
	}
}

// The index follows every write to tasks, through triggers.
func TestSearchIndexFollowsEdits(t *testing.T) {
	b := newBoard(t)
	task := b.add("first name", "")
	if _, err := b.UpdateTask(b.ctx, TaskUpdate{ID: task.ID, By: b.human.ID, Title: "second name"}); err != nil {
		t.Fatal(err)
	}
	if len(b.search(TaskFilter{Query: "first"}).Tasks) != 0 || len(b.search(TaskFilter{Query: "second"}).Tasks) != 1 {
		t.Fatal("the index has the new title only")
	}
	b.finish(task.ID, "done", "")
	b.archive(ArchiveReq{})
	var n int
	_ = b.db.QueryRow(`SELECT count(*) FROM task_search`).Scan(&n)
	if n != 1 || len(b.search(TaskFilter{Query: "second"}).Tasks) != 1 {
		t.Fatalf("archiving keeps one index row: %d", n)
	}
}

// The board, status and assignee narrow the candidates before their cap: a
// busy board elsewhere can't crowd a scoped search's exact match out.
func TestAScopedSearchSeesPastOtherBoardsNoise(t *testing.T) {
	b := newBoard(t)
	target := b.add("Deploy pipeline", strings.Repeat("a long body about something else entirely ", 40))
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < searchCandidates+100; i++ {
		if _, err := tx.Exec(`INSERT INTO tasks(board,title,created_at,updated_at) VALUES('noise','deploy pipeline deploy pipeline',1,1)`); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Claim(b.ctx, b.worker.ID, target.ID); err != nil {
		t.Fatal(err)
	}
	for _, f := range []TaskFilter{{Board: "shop"}, {Board: "*", Assignee: b.worker.Name}, {Board: "*", Status: "doing"},
		{Board: "*", Mine: true, Agent: b.worker.ID}, {Board: "*", Open: true, Assignee: b.worker.Name}} {
		f.Query = "deploy pipeline"
		if p := b.search(f); len(p.Tasks) != 1 || p.Tasks[0].ID != target.ID {
			t.Errorf("%+v: the one task in scope: %v", f, titles(p))
		}
	}
	// Unscoped, the cap still holds.
	if p := b.search(TaskFilter{Query: "deploy pipeline", Limit: maxSearchLimit}); len(p.Tasks)+p.More != searchCandidates {
		t.Errorf("unscoped: %d shown + %d more, want the %d candidates", len(p.Tasks), p.More, searchCandidates)
	}
}

// A successor taking over an archived task under another name is found by
// that name.
func TestSearchFollowsARenameOfArchivedWork(t *testing.T) {
	b := newBoard(t)
	task := b.add("ship the release", "")
	b.finish(task.ID, "done", "")
	b.archive(ArchiveReq{})
	if err := b.Leave(b.ctx, b.worker.ID); err != nil {
		t.Fatal(err)
	}
	b.clock = b.clock.Add(time.Hour)
	// Someone online holds the old name, so the successor keeps its own.
	squatter := reg(t, b.Store, RegisterReq{Harness: "cursor", Project: "shop", AnchorPID: 3})
	if _, err := b.db.Exec(`UPDATE agents SET name=? WHERE id=?`, b.worker.Name, squatter.ID); err != nil {
		t.Fatal(err)
	}
	next := reg(t, b.Store, RegisterReq{Harness: "codex", Name: "successor", Project: "shop", AnchorPID: 2})
	if _, err := b.ContinueAs(b.ctx, next.ID, b.worker.ID); err != nil {
		t.Fatal(err)
	}
	var assignee string
	_ = b.db.QueryRow(`SELECT assignee FROM archived_tasks WHERE id=?`, task.ID).Scan(&assignee)
	if assignee != "successor" {
		t.Fatalf("archived task renamed: %q", assignee)
	}
	if p := b.search(TaskFilter{Query: "successor"}); len(p.Tasks) != 1 || p.Tasks[0].ID != task.ID {
		t.Fatalf("found by the new name: %v", titles(p))
	}
	if p := b.search(TaskFilter{Query: b.worker.Name}); len(p.Tasks) != 0 {
		t.Fatalf("not by the old one: %v", titles(p))
	}
}

func TestShortQueriesScan(t *testing.T) {
	b := newBoard(t)
	b.add("UI polish", "")
	b.add("backend", "")
	if got := titles(b.search(TaskFilter{Query: "ui"})); len(got) != 1 || got[0] != "UI polish" {
		t.Fatalf("two letters: %v", got)
	}
}

func TestNearIndex(t *testing.T) {
	for _, c := range []struct {
		text, w string
		want    bool
	}{
		{"the reservations table", "reservations", true},
		{"the reservations table", "reservatoins", true}, // swap
		{"the reservations table", "reservaions", true},  // missing
		{"the reservations table", "reservaxtions", true},
		{"the reservations table", "reservaxxions", false},
		{"short", "shxrt", true},
		{"", "abcde", false},
	} {
		if got := nearIndex([]rune(c.text), []rune(c.w)) >= 0; got != c.want {
			t.Errorf("nearIndex(%q, %q) = %v", c.text, c.w, got)
		}
	}
}

// A hub made by v8 opens with the new code: its tasks kept, the archive
// table there, and its tasks found by search.
func TestMigrateFromV8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{schema, schemaV2} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range addedColumns {
		if _, err := db.Exec(c.ddl); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	if _, err := db.Exec(`INSERT INTO meta(key,value) VALUES('schema','8');
		INSERT INTO tasks(board,title,body,status,created_at,updated_at) VALUES('shop','Reservación del hotel','con desayuno','todo',?,?);
		INSERT INTO tasks(board,title,status,result,created_at,updated_at) VALUES('shop','Old work','done','fine',?,?)`, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s, err := Open(path, Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v string
	_ = s.db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&v)
	if v != fmt.Sprint(schemaVersion) {
		t.Fatalf("schema marker: %s", v)
	}
	list, err := s.ListTasks(t.Context(), TaskFilter{Board: "*"})
	if err != nil || len(list) != 2 {
		t.Fatalf("tasks kept: %+v %v", list, err)
	}
	p, err := s.TaskList(t.Context(), TaskFilter{Board: "*", Query: "reservacion desayuno"})
	if err != nil || len(p.Tasks) != 1 {
		t.Fatalf("old tasks are indexed: %+v %v", p, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM archived_tasks`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("archive table: %v", err)
	}
	if !upgraded(s.db) {
		t.Fatal("upgraded() sees the v9 tables")
	}
	// Opening again neither fails nor indexes twice.
	s2, err := Open(path, Options{Stale: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
	if _, err := s.db.Exec(`UPDATE meta SET value='8' WHERE key='schema'`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(s.db); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRow(`SELECT count(*) FROM task_search`).Scan(&n)
	if n != 2 {
		t.Fatalf("the backfill runs once per task: %d rows", n)
	}
}
