package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/transcript"
)

// aSession writes a transcript of two requests for the project: 1,000
// tokens to start with, 4,000 after a Bash result.
func aSession(t *testing.T, userHome, project, id string) {
	t.Helper()
	dir := filepath.Join(userHome, ".claude", "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(project, "-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	usage := func(n int) string { return fmt.Sprintf(`"usage":{"input_tokens":%d,"output_tokens":0}`, n) }
	lines := []string{
		`{"type":"custom-title","customTitle":"Fix\u001b[2J the cart"}`,
		`{"type":"user","message":{"content":"go"}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-x","content":[{"type":"tool_use","id":"c1","name":"Bash","input":{}}],` + usage(1000) + `}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c1","content":"` + strings.Repeat("b", 900) + `"}]}}`,
		`{"type":"assistant","message":{"id":"m2","model":"claude-x","content":[],` + usage(4000) + `}}`,
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestContextShowsWhatFillsTheWindow(t *testing.T) {
	_, userHome, project := sandbox(t)
	t.Setenv("HOME", userHome)
	t.Setenv("USERPROFILE", userHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if out, _, code := cli(t, "", "context"); code != 0 || !strings.Contains(out, "no Claude Code session") {
		t.Fatalf("none: %d %q", code, out)
	}
	wd, err := os.Getwd() // the project as the system spells it
	if err != nil {
		t.Fatal(err)
	}
	aSession(t, userHome, wd, "abcdef12-0000")
	aSession(t, userHome, filepath.Join(project, "other"), "abcdef99-0000")

	out, _, code := cli(t, "", "context")
	if code != 0 || !strings.Contains(out, "active in the last 24h: 1") || !strings.Contains(out, "abcdef12") || !strings.Contains(out, "Fix [2J the cart") ||
		!strings.Contains(out, "Bash results 100%") || !strings.Contains(out, "a session holds 1.0k tokens (median)") {
		t.Fatalf("the project's: %d %q", code, out)
	}
	if out, _, _ := cli(t, "", "context", "--all"); !strings.Contains(out, "active in the last 24h: 2") {
		t.Fatalf("all: %q", out)
	}
	out, _, code = cli(t, "", "context", "abcdef12")
	if code != 0 || !strings.Contains(out, "now 4.0k tokens") || !strings.Contains(out, "Its first request: 1.0k tokens") {
		t.Fatalf("one: %d %q", code, out)
	}
	out, _, _ = cli(t, "", "context", "abcdef12-0000", "--json")
	var f transcript.Fill
	if err := json.Unmarshal([]byte(out), &f); err != nil || f.Now != 4000 || f.Start != 1000 {
		t.Fatalf("json: %v %q", err, out)
	}
	if _, errOut, code := cli(t, "", "context", "abcdef"); code != 1 || !strings.Contains(errOut, `2 sessions start with "abcdef"`) {
		t.Fatalf("ambiguous: %d %q", code, errOut)
	}
	if _, errOut, code := cli(t, "", "context", "nobody@api"); code != 1 || !strings.Contains(errOut, `no session "nobody@api"`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
	if _, errOut, code := cli(t, "", "context", "--hours", "0"); code != 1 || !strings.Contains(errOut, "usage") {
		t.Fatalf("hours: %d %q", code, errOut)
	}
}
