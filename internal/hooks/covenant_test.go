package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/covenant"
	"github.com/romanpert/ainterlinked/internal/hub"
)

// A realistic covenant: a short core and rules for eight areas.
const repoCovenant = `# Covenant

## *
- Run the tests before committing; never commit a red build.
- Reserve files before editing them; release them when done.

## db/migrations/**
- Never edit a merged migration: add a new one with its down step.
## src/api/**
- Keep handlers thin: validation at the boundary, logic in services.
- Every new endpoint needs a test and rate limiting.
## src/auth/**
- No secrets in code; tokens come from the environment.
- Changes here need a security review task on the board.
## src/ui/**
- Use the design tokens (bg-card, text-foreground), never raw colors.
## infra/**, .github/**
- Ask the owner before changing CI or deployment.
## package.json, go.mod
- New dependencies need a one-line reason in the commit message.
## docs/**
- Docs follow the code: update them in the same commit.
## .interlinked/covenant.md
- Propose changes to the owner; don't edit the covenant without their OK.
`

func writeCovenant(t *testing.T, dir, src string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(covenant.File))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func edit(path string) map[string]any {
	return map[string]any{"tool_name": "Edit", "tool_input": map[string]any{"file_path": path}}
}

func TestCovenantBriefingCarriesOnlyTheCore(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	_, out := e.as("1", "claude-code", "SessionStart", nil)
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "never commit a red build") || !strings.Contains(ctx, "rules for 8 areas") {
		t.Fatalf("briefing: %s", ctx)
	}
	if strings.Contains(ctx, "merged migration") {
		t.Fatal("area rules must not be in the briefing")
	}
}

func TestSmallCovenantGoesWholeIntoTheBriefing(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, "## *\n- Tests before commits.\n## db/migrations/**\n- Never edit a merged migration.\n")
	_, out := e.as("1", "claude-code", "SessionStart", nil)
	ctx := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "Tests before commits") || !strings.Contains(ctx, "Never edit a merged migration") {
		t.Fatalf("briefing: %s", ctx)
	}
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "db", "migrations", "1.sql"))); out != nil {
		t.Fatalf("already briefed: no held-back edit, no retry: %v", out)
	}
}

func TestReadingAFileBringsItsAreaRules(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	e.as("1", "claude-code", "SessionStart", nil)
	mig := filepath.Join(e.dir, "db", "migrations", "0004_totals.sql")
	_, out := e.as("1", "claude-code", "PostToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": mig}})
	ctx, _ := out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)
	if !strings.Contains(ctx, "Never edit a merged migration") {
		t.Fatalf("read: %v", out)
	}
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(mig)); out != nil {
		t.Fatalf("seen on read: the edit must not be held back: %v", out)
	}
	if _, out = e.as("1", "claude-code", "PostToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": mig}}); out != nil {
		t.Fatalf("once per session: %v", out)
	}
}

func TestCovenantAreaRulesShownOncePerSession(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	e.as("1", "claude-code", "SessionStart", nil)
	mig := filepath.Join(e.dir, "db", "migrations", "0004_totals.sql")

	_, out := e.as("1", "claude-code", "PreToolUse", edit(mig))
	reason, _ := out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
	if !strings.Contains(reason, "Never edit a merged migration") || strings.Contains(reason, "design tokens") {
		t.Fatalf("first edit in the area: %v", out)
	}
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(mig)); out != nil {
		t.Fatalf("the retry must go through: %v", out)
	}
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "db", "migrations", "0005.sql"))); out != nil {
		t.Fatalf("same area, already shown: %v", out)
	}
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "README.md"))); out != nil {
		t.Fatalf("no rules for README.md: %v", out)
	}
	// Relative paths are resolved from the tool's cwd (a patch from a
	// subfolder of the repo: the git root is the project).
	if err := os.MkdirAll(filepath.Join(e.dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(e.dir, "src")
	_, out = e.as("1", "codex", "PreToolUse", map[string]any{"cwd": sub, "tool_name": "apply_patch",
		"tool_input": map[string]any{"command": "*** Begin Patch\n*** Update File: ui/Button.tsx\n@@\n-a\n+b\n*** End Patch"}})
	if out == nil || !strings.Contains(out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string), "design tokens") {
		t.Fatalf("relative path from a subfolder: %v", out)
	}

	// /clear or compaction end the session: the model lost the rules, so
	// they are shown again.
	e.as("1", "claude-code", "SessionEnd", nil)
	e.as("1", "claude-code", "SessionStart", nil)
	if _, out = e.as("1", "claude-code", "PreToolUse", edit(mig)); out == nil {
		t.Fatal("after the session ended the rules must be shown again")
	}
	// A changed covenant is news too.
	writeCovenant(t, e.dir, repoCovenant+"\n## db/migrations/**\n- Name migrations NNNN_verb_noun.sql.\n")
	_, out = e.as("1", "claude-code", "PreToolUse", edit(mig))
	if out == nil || !strings.Contains(out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string), "NNNN_verb_noun") {
		t.Fatalf("an amended covenant must be shown: %v", out)
	}
}

func TestCovenantNoticeInEveryDialect(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	f := func(name string) string { return filepath.Join(e.dir, "src", "auth", name) }
	cases := []struct {
		pid, harness, event string
		payload             map[string]any
		reason              func(map[string]any) string
	}{
		{"2", "codex", "PreToolUse", map[string]any{"tool_name": "apply_patch", "tool_input": map[string]any{"command": "*** Begin Patch\n*** Update File: src/auth/token.go\n@@\n-a\n+b\n*** End Patch"}},
			func(o map[string]any) string {
				return o["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string)
			}},
		{"3", "cursor", "preToolUse", edit(f("cursor.go")), func(o map[string]any) string { s, _ := o["agent_message"].(string); return s }},
		{"4", "gemini", "BeforeTool", map[string]any{"tool_name": "write_file", "tool_input": map[string]any{"file_path": f("gemini.go")}},
			func(o map[string]any) string { s, _ := o["reason"].(string); return s }},
		{"5", "copilot", "preToolUse", map[string]any{"toolName": "edit", "toolArgs": `{"path":"` + filepath.ToSlash(f("copilot.go")) + `"}`},
			func(o map[string]any) string { s, _ := o["permissionDecisionReason"].(string); return s }},
		{"6", "opencode", "PreToolUse", map[string]any{"tool_name": "edit", "tool_input": map[string]any{"filePath": f("opencode.go")}},
			func(o map[string]any) string { s, _ := o["deny"].(string); return s }},
	}
	for _, c := range cases {
		e.as(c.pid, c.harness, "SessionStart", nil)
		_, out := e.as(c.pid, c.harness, c.event, c.payload)
		if out == nil || !strings.Contains(c.reason(out), "No secrets in code") {
			t.Errorf("%s: covenant not shown: %v", c.harness, out)
		}
	}
}

func TestReservationWinsOverCovenant(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	e.as("1", "claude-code", "SessionStart", nil)
	owner := e.agent("1")
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: owner.ID, Paths: []string{"src/api/"}, Reason: "task #2"}, &res); err != nil {
		t.Fatal(err)
	}
	e.as("2", "claude-code", "SessionStart", nil)
	_, out := e.as("2", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "src", "api", "cart.go")))
	if r := out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string); !strings.Contains(r, "reserved by") {
		t.Fatalf("someone else's reservation comes first: %s", r)
	}
	// The owner of the reservation still gets the area's rules on its first edit.
	_, out = e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "src", "api", "cart.go")))
	if out == nil || !strings.Contains(out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string), "rate limiting") {
		t.Fatalf("owner: %v", out)
	}
}

func TestCovenantOffAndAbsent(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	if _, out := e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "db", "migrations", "1.sql"))); out != nil {
		t.Fatalf("no covenant, no notice: %v", out)
	}
	writeCovenant(t, e.dir, repoCovenant)
	e.cfg.Covenant.Show = false
	_, out := e.as("1", "claude-code", "SessionStart", nil)
	if strings.Contains(out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string), "covenant") {
		t.Fatal("covenant off: nothing in the briefing")
	}
	if _, out := e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "db", "migrations", "1.sql"))); out != nil {
		t.Fatalf("covenant off: %v", out)
	}
}

// TestCovenantCost measures what the covenant costs in context and in hook
// time, against the naive alternative of putting the whole file in every
// session (as an AGENTS.md is).
func TestCovenantCost(t *testing.T) {
	// A bigger repo: the realistic covenant plus 24 more areas.
	big := repoCovenant
	for i := 0; i < 24; i++ {
		big += "## pkg/module" + strconv.Itoa(i) + "/**\n- Module " + strconv.Itoa(i) + " owns its schema; talk to its owner before changing the public API.\n"
	}
	for _, tc := range []struct {
		name, src string
	}{{"8 areas", repoCovenant}, {"32 areas", big}} {
		e := setup(t)
		writeCovenant(t, e.dir, tc.src)
		before := setup(t)
		_, out := before.as("1", "claude-code", "SessionStart", nil)
		bare := len(out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string))
		_, out = e.as("1", "claude-code", "SessionStart", nil)
		brief := len(out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string)) - bare
		// A typical session reads, then edits, files in a couple of areas.
		notices, held := 0, 0
		for _, f := range []string{"src/api/cart.go", "db/migrations/9.sql"} {
			p := filepath.Join(e.dir, filepath.FromSlash(f))
			if _, out = e.as("1", "claude-code", "PostToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": p}}); out != nil {
				notices += len(out["hookSpecificOutput"].(map[string]any)["additionalContext"].(string))
			}
			if _, out = e.as("1", "claude-code", "PreToolUse", edit(p)); out != nil {
				held++
			}
		}
		whole := len(tc.src)
		t.Logf("%s, covenant %d chars: briefing +%d, area notices %d, total %d = %.0f%% of the whole file (≈%d vs ≈%d tokens); edits held back: %d",
			tc.name, whole, brief, notices, brief+notices, 100*float64(brief+notices)/float64(whole), (brief+notices)/4, whole/4, held)
		if held != 0 {
			t.Errorf("%s: a file read first must not cost a retry", tc.name)
		}
		if tc.name == "32 areas" && brief+notices > whole/3 {
			t.Errorf("%s: scoping should cost well under the whole file, got %d of %d", tc.name, brief+notices, whole)
		}
	}

	e := setup(t)
	writeCovenant(t, e.dir, big)

	// Hook time with the covenant read on every edit.
	raw, _ := json.Marshal(map[string]any{"cwd": e.dir, "tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(e.dir, "src", "api", "x.go")}})
	var times []time.Duration
	for i := 0; i < 15; i++ {
		t0 := time.Now()
		if _, err := Handle(context.Background(), e.cfg, e.c, "claude-code", "PreToolUse", ParseInput(raw)); err != nil {
			t.Fatal(err)
		}
		times = append(times, time.Since(t0))
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	t.Logf("PreToolUse with covenant: median %s, max %s", times[7], times[14])
	if times[7] > 50*time.Millisecond {
		t.Errorf("covenant check too slow: %s", times[7])
	}
}

func TestCovenantDeliveriesAreLogged(t *testing.T) {
	e := setup(t)
	writeCovenant(t, e.dir, repoCovenant)
	e.as("1", "claude-code", "SessionStart", nil)
	e.as("1", "claude-code", "PostToolUse", map[string]any{"tool_name": "Read", "tool_input": map[string]any{"file_path": filepath.Join(e.dir, "src", "ui", "a.tsx")}})
	_, out := e.as("1", "claude-code", "PreToolUse", edit(filepath.Join(e.dir, "docs", "x.md")))
	if r := out["hookSpecificOutput"].(map[string]any)["permissionDecisionReason"].(string); !strings.Contains(r, "edit NOT applied") {
		t.Fatalf("a held edit must say it wasn't applied: %s", r)
	}
	b, err := os.ReadFile(filepath.Join(os.Getenv("INTERLINKED_HOME"), "covenant.log"))
	if err != nil {
		t.Fatal(err)
	}
	var routes []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var r struct {
			Route string   `json:"route"`
			Areas []string `json:"areas"`
		}
		if err := json.Unmarshal([]byte(l), &r); err != nil {
			t.Fatal(err)
		}
		routes = append(routes, r.Route+" "+strings.Join(r.Areas, ","))
	}
	if strings.Join(routes, "|") != "context src/ui/**|held docs/**" {
		t.Fatalf("log: %q", routes)
	}
}

// oldHub is a remote hub from before touch_file.
type oldHub struct{ hub.Client }

func (o oldHub) Call(ctx context.Context, method string, params, out any) error {
	if method == "touch_file" {
		return errors.New(`unknown method "touch_file"`)
	}
	return o.Client.Call(ctx, method, params, out)
}

func TestReservationCheckFallsBackOnAnOlderHub(t *testing.T) {
	e := setup(t)
	e.as("1", "claude-code", "SessionStart", nil)
	owner := e.agent("1")
	var res core.LockResult
	if err := e.c.Call(context.Background(), "reserve", core.LockReq{Agent: owner.ID, Paths: []string{"main.go"}, Reason: "task #9"}, &res); err != nil {
		t.Fatal(err)
	}
	e.as("2", "codex", "SessionStart", nil)
	t.Setenv("INTERLINKED_ANCHOR_PID", "2")
	raw, _ := json.Marshal(map[string]any{"cwd": e.dir, "tool_name": "Edit", "tool_input": map[string]any{"file_path": filepath.Join(e.dir, "main.go")}})
	r, err := Handle(context.Background(), e.cfg, oldHub{e.c}, "codex", "PreToolUse", ParseInput(raw))
	if err != nil || !strings.Contains(r.Deny, "main.go is reserved by "+owner.Name) {
		t.Fatalf("older hub: %+v %v", r, err)
	}
}
