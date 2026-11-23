package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
)

func TestTasksBoardSearchAndArchive(t *testing.T) {
	home, _, project := sandbox(t)
	for _, title := range []string{"Reservación de sala", "ship the menu"} {
		if out, errOut, code := cli(t, "", "task", "add", title, "--board", "web"); code != 0 {
			t.Fatalf("task add: %q %q", out, errOut)
		}
	}
	if _, errOut, code := cli(t, "", "task", "done", "2", "--result", "shipped"); code != 0 {
		t.Fatal(errOut)
	}
	out, _, _ := cli(t, "", "tasks")
	if !strings.Contains(out, "Reservación") || strings.Contains(out, "ship the menu") || !strings.Contains(out, "1 closed (status=done") {
		t.Fatalf("tasks: %q", out)
	}
	if out, _, _ = cli(t, "", "tasks", "--status", "done"); !strings.Contains(out, "ship the menu") {
		t.Fatalf("tasks --status done: %q", out)
	}
	if out, _, _ = cli(t, "", "tasks", "--query", "reservacion"); !strings.Contains(out, "Reservación de sala") {
		t.Fatalf("tasks --query: %q", out)
	}
	if out, _, _ = cli(t, "", "tasks", "archive", "--older-than", "7d"); !strings.Contains(out, "Nothing to archive") {
		t.Fatalf("closed just now: %q", out)
	}
	if _, errOut, code := cli(t, "", "tasks", "archive", "--older-than", "soon"); code != 1 || !strings.Contains(errOut, "7d") {
		t.Fatalf("bad age: %q", errOut)
	}
	out, errOut, code := cli(t, "", "tasks", "archive")
	if code != 0 || !strings.Contains(out, "Archived 1 closed task(s)") || !strings.Contains(out, "#2 → ") ||
		!strings.Contains(mustRead(t, filepath.Join(home, "archive", "web.md")), "Result:\n\n> shipped") {
		t.Fatalf("archive: %q %q", out, errOut)
	}
	if out, _, _ = cli(t, "", "tasks"); strings.Contains(out, "closed") {
		t.Fatalf("archived tasks leave the board: %q", out)
	}
	if out, _, _ = cli(t, "", "tasks", "--query", "ship"); !strings.Contains(out, "ship the menu") || !strings.Contains(out, "archived") {
		t.Fatalf("search finds archived: %q", out)
	}
	// --to is the caller's path, not the hub's working directory.
	if _, errOut, code := cli(t, "", "task", "cancel", "1"); code != 0 {
		t.Fatal(errOut)
	}
	if _, errOut, code := cli(t, "", "tasks", "archive", "--to", "old.md"); code != 0 {
		t.Fatal(errOut)
	}
	if !strings.Contains(mustRead(t, filepath.Join(project, "old.md")), "Reservación de sala (cancelled)") {
		t.Fatal("--to old.md")
	}
}

// A file that couldn't be written is named, with its tasks, not hidden
// behind what was archived.
func TestArchiveReportNamesFailedFiles(t *testing.T) {
	res := core.ArchiveResult{Count: 1, Files: []core.ArchiveFile{{Path: "/a/shop.md", IDs: []int64{1}}},
		Failed: []core.ArchiveFailure{{Path: "/a/web.md", IDs: []int64{2, 3}, Error: "is a directory"}}}
	out := archiveReport(res)
	if !strings.Contains(out, "Archived 1 closed task(s)") || !strings.Contains(out, "not archived: #2 #3 → /a/web.md: is a directory") {
		t.Fatalf("report: %q", out)
	}
	res.Count, res.Files = 0, nil
	if out = archiveReport(res); strings.Contains(out, "Nothing to archive") || !strings.Contains(out, "not archived: #2 #3") {
		t.Fatalf("only failures: %q", out)
	}
}

func TestParseAge(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "7d": 7 * 24 * time.Hour, "36h": 36 * time.Hour, "1.5d": 36 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := parseAge(in); err != nil || got != want {
			t.Errorf("parseAge(%q) = %v, %v", in, got, err)
		}
	}
	for _, in := range []string{"-1d", "0d", "week", "7"} {
		if _, err := parseAge(in); err == nil {
			t.Errorf("parseAge(%q) should fail", in)
		}
	}
}
