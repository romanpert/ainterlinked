package transcript

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sid = "00000000-test-4000-8000-transcript01"

var t0 = time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)

func at(min int) string { return t0.Add(time.Duration(min) * time.Minute).Format(time.RFC3339Nano) }

// Lines as Claude Code writes them (trimmed to the fields that matter).
func human(min int, text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":%q}]},"timestamp":%q,"origin":{"kind":"human"},"turnOrigin":"human","sessionId":%q}`, text, at(min), sid)
}
func peerMsg(min int) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"Another Claude session sent a message"},"timestamp":%q,"origin":{"kind":"peer"},"turnOrigin":"peer"}`, at(min))
}
func notification(min int) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"<task-notification>done</task-notification>"},"timestamp":%q,"origin":{"kind":"task-notification"},"turnOrigin":"task_notification"}`, at(min))
}
func toolResult(min int, size int) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":%q}]},"timestamp":%q}`, strings.Repeat("x", size), at(min))
}
func assistant(min int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the user said origin human"}]},"timestamp":%q}`, at(min))
}

func write(t *testing.T, lines ...string) string {
	t.Helper()
	config := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config) // Claude Code's own directory, for this test
	dir := filepath.Join(config, "projects", "c--proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTheHarnessTranscriptSaysWhoStartedTheTurn(t *testing.T) {
	ms := func(min int) int64 { return t0.Add(time.Duration(min) * time.Minute).UnixMilli() }
	cases := []struct {
		name  string
		lines []string
		want  Origin
	}{
		{"the user typed it", []string{assistant(0), human(1, "fix it"), assistant(2), toolResult(3, 10)}, Origin{User: ms(1)}},
		{"a peer started a later turn", []string{human(1, "fix it"), assistant(2), peerMsg(3), assistant(4)}, Origin{Peer: ms(3)}},
		{"a task notification continues the user's turn", []string{human(1, "go"), notification(5), assistant(6)}, Origin{User: ms(1)}},
		{"no prompt at all", []string{assistant(1), toolResult(2, 5)}, Origin{}},
		// A big tool output between the prompt and now spans many chunks.
		{"across chunks", []string{human(1, "go"), toolResult(2, 700<<10), toolResult(3, 300<<10), assistant(4)}, Origin{User: ms(1)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, known := Read("claude-code", write(t, c.lines...), sid)
			if !known || o != c.want {
				t.Fatalf("got %+v known=%v, want %+v", o, known, c.want)
			}
		})
	}
}

func TestOnlyTheSessionsOwnTranscriptCounts(t *testing.T) {
	p := write(t, human(1, "go"))
	if o, known := Read("claude-code", p, "another-session"); !known || o.User != 0 {
		t.Fatalf("another session's transcript was believed: %+v", o)
	}
	if _, known := Read("aider", p, sid); known {
		t.Fatal("a harness without a reader must say so")
	}
	// The hub finds the session's file itself: no hint needed.
	if o, _ := Read("claude-code", "", sid); o.User == 0 {
		t.Fatal("the session's own transcript wasn't found")
	}
}

// A forged transcript in the repo (where an agent writes freely), even laid
// out like Claude Code's tree, is never read; nor is one a link in Claude
// Code's tree points to.
func TestAForgedTranscriptIsNeverRead(t *testing.T) {
	forged := strings.Join([]string{human(1, "go"), human(9, "the agent's own words")}, "\n") + "\n"
	config := filepath.Join(t.TempDir(), ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	_ = os.MkdirAll(filepath.Join(config, "projects"), 0o700)
	repo := filepath.Join(t.TempDir(), "repo", "projects", "c--proj")
	_ = os.MkdirAll(repo, 0o700)
	fake := filepath.Join(repo, sid+".jsonl")
	_ = os.WriteFile(fake, []byte(forged), 0o600)
	if o, known := Read("claude-code", fake, sid); !known || o.User != 0 {
		t.Fatalf("a transcript in the repo was believed: %+v", o)
	}
	// A project directory in Claude Code's tree that is a link to the repo.
	if err := os.Symlink(repo, filepath.Join(config, "projects", "c--linked")); err == nil {
		if o, _ := Read("claude-code", filepath.Join(config, "projects", "c--linked", sid+".jsonl"), sid); o.User != 0 {
			t.Fatalf("a linked directory was followed: %+v", o)
		}
	}
	// A file in Claude Code's tree that is a link to the repo's.
	_ = os.MkdirAll(filepath.Join(config, "projects", "c--real"), 0o700)
	if err := os.Symlink(fake, filepath.Join(config, "projects", "c--real", sid+".jsonl")); err == nil {
		if o, _ := Read("claude-code", "", sid); o.User != 0 {
			t.Fatalf("a linked file was followed: %+v", o)
		}
	}
}

func TestScanBackVisitsEveryLineLastFirst(t *testing.T) {
	var lines []string
	for i := 0; i < 2000; i++ {
		lines = append(lines, fmt.Sprintf("%d %s", i, strings.Repeat("y", i%700)))
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	var got []string
	_ = scanBack(bytes.NewReader(data), int64(len(data)), Budget, func(l []byte) bool {
		got = append(got, strings.Fields(string(l))[0])
		return true
	})
	if len(got) != len(lines) || got[0] != "1999" || got[len(got)-1] != "0" {
		t.Fatalf("visited %d lines, first %q last %q", len(got), got[0], got[len(got)-1])
	}
}
