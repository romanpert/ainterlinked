package render

import (
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/core"
)

func TestTaskAgeIsShownFromADay(t *testing.T) {
	got := Tasks([]core.Task{
		{ID: 12, Status: "todo", Priority: 2, Title: "Title", Board: "shop", Untouched: 21},
		{ID: 13, Status: "todo", Priority: 2, Title: "Fresh", Board: "shop"},
	})
	want := "#12 [todo] P2 Title · shop · 21d untouched\n#13 [todo] P2 Fresh · shop"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestBoardsCountForgottenTasksOnlyWhenAsked(t *testing.T) {
	b := core.Board{Name: "shop", Counts: map[string]int{"todo": 5}, Total: 5}
	if got := Boards([]core.Board{b}); got != "shop: 5 tasks (todo 5)" {
		t.Fatalf("off: %q", got)
	}
	b.Stale, b.StaleDays = 4, 14
	if got := Boards([]core.Board{b}); got != "shop: 5 tasks (todo 5) · 4 untouched 14d+" {
		t.Fatalf("on: %q", got)
	}
}

func TestStaleTasksSaysWhatToDo(t *testing.T) {
	if got := StaleTasks(core.StaleList{Days: 14}); got != "No open task untouched for 14d+." {
		t.Fatalf("empty: %q", got)
	}
	got := StaleTasks(core.StaleList{Days: 7, Tasks: []core.Task{
		{ID: 3, Status: "review", Priority: 1, Title: "Old", Board: "shop", Untouched: 30, CreatedBy: "codex@shop", Assignee: "claude@shop"},
		{ID: 9, Status: "todo", Priority: 2, Title: "Newer", Board: "web", Untouched: 8},
	}})
	lines := strings.Split(got, "\n")
	if len(lines) != 4 || lines[0] != "2 open task(s) untouched for 7d+, oldest first:" ||
		lines[1] != "#3 [review] P1 Old · 30d untouched · shop · by codex@shop · @claude@shop" ||
		lines[2] != "#9 [todo] P2 Newer · 8d untouched · web" ||
		!strings.Contains(lines[3], "ainterlinked task cancel <id>") || !strings.Contains(lines[3], "ainterlinked task reopen <id>") {
		t.Fatalf("got:\n%s", got)
	}
}
