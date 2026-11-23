package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `tasks stale` is a command, not a board named "stale"; its default
// follows [tasks] stale_after_days, else 14 days.
func TestTasksStale(t *testing.T) {
	home, _, _ := sandbox(t)
	if out, errOut, code := cli(t, "", "task", "add", "fresh", "--board", "web"); code != 0 {
		t.Fatalf("task add: %d %q %q", code, out, errOut)
	}
	out, errOut, code := cli(t, "", "tasks", "stale")
	if code != 0 || strings.TrimSpace(out) != "No open task untouched for 14d+." {
		t.Fatalf("default: %d %q %q", code, out, errOut)
	}
	if out, _, _ = cli(t, "", "tasks", "stale", "--days", "3", "--board", "web"); strings.TrimSpace(out) != "No open task untouched for 3d+." {
		t.Fatalf("--days: %q", out)
	}
	if _, errOut, code = cli(t, "", "tasks", "stale", "--days", "-1"); code != 1 || !strings.Contains(errOut, "--days") {
		t.Fatalf("negative days: %d %q", code, errOut)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[tasks]\nstale_after_days = 9\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, _, _ = cli(t, "", "tasks", "stale"); strings.TrimSpace(out) != "No open task untouched for 9d+." {
		t.Fatalf("the user's setting: %q", out)
	}
}
