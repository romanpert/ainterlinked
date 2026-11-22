package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Entries as Claude Code writes them, reduced to what ReadFill reads.
func answered(id string, size, out, thinking int64, blocks string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"model":"claude-x","content":[%s],"usage":{"input_tokens":2,"cache_creation_input_tokens":%d,"cache_read_input_tokens":10,"output_tokens":%d,"output_tokens_details":{"thinking_tokens":%d}}}}`,
		id, blocks, size-12, out, thinking)
}

func call(id, tool, input string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":{"x":%q}}`, id, tool, input)
}

func result(id, text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":%q,"content":%q}]}}`, id, text)
}

func put(kind, text string) string {
	return fmt.Sprintf(`{"type":"attachment","attachment":{"type":%q,"content":[%q]},"rendered":[{"content":%q}],"renderedRole":"system"}`, kind, text, text)
}

func prompt(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"content":%q}}`, text)
}

func shape(system, tools string) string {
	return fmt.Sprintf(`{"type":"api-request-shape","shape":{"system":[{"text":%q}],"tools":[{"name":"Big","d":%q},{"name":"Small"}]}}`, system, tools)
}

func fillOf(t *testing.T, lines ...string) Fill {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s-1.jsonl")
	appendTo(t, path, lines...)
	f, err := ReadFill(path)
	if err != nil {
		t.Fatal(err)
	}
	if f.Session != "s-1" {
		t.Fatalf("session %q", f.Session)
	}
	return f
}

func tokens(ps []Piece, name string) int64 {
	for _, p := range ps {
		if p.Name == name {
			return p.Tokens
		}
	}
	return 0
}

func sum(ps []Piece) (n int64) {
	for _, p := range ps {
		n += p.Tokens
	}
	return n
}

// near: pieces are rounded down one by one.
func near(t *testing.T, what string, got, want int64) {
	t.Helper()
	if got > want || got < want-10 {
		t.Fatalf("%s: %d, want about %d", what, got, want)
	}
}

// The first request is split among all it carried, and its pieces add up to
// the API's own count.
func TestWhatASessionStartsWith(t *testing.T) {
	f := fillOf(t,
		put("skill_listing", strings.Repeat("s", 1000)),
		`{"type":"attachment","attachment":{"type":"instructions","files":[{"path":"/a/CLAUDE.md","content":"`+strings.Repeat("i", 3000)+`"},{"path":"/a/rules.md","content":"`+strings.Repeat("i", 1000)+`"}]},"rendered":[{"content":"`+strings.Repeat("i", 4000)+`"}]}`,
		put("hook_additional_context", `[ainterlinked] You are "claude@api"`),
		prompt("go"),
		shape(strings.Repeat("y", 2000), strings.Repeat("t", 6000)),
		answered("m1", 100_000, 50, 0, `{"type":"text","text":"ok"}`),
	)
	if f.Start != 100_000 || f.Now != 100_000 || f.Peak != 100_000 || f.Model != "claude-x" {
		t.Fatalf("%+v", f)
	}
	near(t, "its pieces", sum(f.Begin), 100_000)
	if f.Begin[0].Name != PieceTools || tokens(f.Begin, PieceInstructions) < 2*tokens(f.Begin, "skill list") {
		t.Fatalf("largest first, by length: %+v", f.Begin)
	}
	if tokens(f.Begin, PieceAinterlinked) == 0 || tokens(f.Begin, "prompts") == 0 || tokens(f.Begin, "hooks") != 0 {
		t.Fatalf("%+v", f.Begin)
	}
	if f.Tools[0].Name != "Big" || f.Files[0].Name != "/a/CLAUDE.md" {
		t.Fatalf("%+v %+v", f.Tools, f.Files)
	}
	near(t, "its files", sum(f.Files), tokens(f.Begin, PieceInstructions))
	near(t, "its tools", sum(f.Tools), tokens(f.Begin, PieceTools))
}

// Without the record of a request's system prompt and tools, the rest is
// said to be unrecorded, not blamed on what is recorded.
func TestAnUnrecordedStartIsSaidToBe(t *testing.T) {
	f := fillOf(t, prompt(strings.Repeat("p", 3000)), answered("m1", 50_000, 5, 0, ""))
	near(t, "prompts", tokens(f.Begin, "prompts"), 1000)
	near(t, "the rest", tokens(f.Begin, PieceUnrecorded), 49_000)
}

// Between two requests, the reply takes its own output tokens and the rest
// is shared among what was added.
func TestWhatASessionGrowsBy(t *testing.T) {
	f := fillOf(t,
		prompt("go"),
		answered("m1", 10_000, 40, 400, call("c1", "Bash", "ls")), // streamed: the last usage counts
		answered("m1", 10_000, 1000, 400, call("c2", "mcp__ainterlinked__read_inbox", "")),
		`{"type":"attachment","attachment":{"type":"hook_success","content":"noise","stdout":"`+strings.Repeat("n", 90_000)+`"}}`,
		`{"type":"user","isSidechain":true,"message":{"content":"`+strings.Repeat("n", 90_000)+`"}}`,
		result("c1", strings.Repeat("b", 3000)),
		result("c2", strings.Repeat("m", 500)),
		put("hook_additional_context", "[ainterlinked] "+strings.Repeat("m", 485)),
		answered("m2", 16_000, 10, 0, `{"type":"text","text":"done"}`),
	)
	near(t, "in all", sum(f.Grown), 6000)
	near(t, "thinking", tokens(f.Grown, "its own thinking"), 400)
	near(t, "Bash output", tokens(f.Grown, "Bash results"), 3750) // 3000 of 4000 chars × 5000 tokens
	if n := tokens(f.Grown, PieceAinterlinked); n < 1250 || n > 1850 {
		t.Fatalf("its calls, their results and its hooks: %d in %+v", n, f.Grown)
	}
	if f.Grown[0].Name != "Bash results" || f.Now != 16_000 || f.Peak != 16_000 {
		t.Fatalf("%+v", f)
	}
}

// A compaction isn't growth, nor is the context a harness drops by itself.
func TestCompactionsAreNotGrowth(t *testing.T) {
	f := fillOf(t,
		prompt("go"),
		answered("m1", 100_000, 10, 0, ""),
		prompt("more"),
		answered("m2", 300_000, 10, 0, ""),
		`{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":301000,"postTokens":9}}`,
		prompt("the summary"),
		answered("m3", 160_000, 10, 0, ""),
		prompt("x"),
		answered("m4", 150_000, 10, 0, ""), // dropped old results
		prompt("y"),
		answered("m5", 151_000, 10, 0, ""),
		`{"type":"assistant","message":{"id":"e","model":"<synthetic>","content":[{"type":"text","text":"API error"}],"usage":{"input_tokens":0}}}`,
	)
	near(t, "growth", sum(f.Grown), 201_000)
	if len(f.Compactions) != 1 || f.Compactions[0] != (Compaction{Auto: true, From: 301_000, To: 160_000}) {
		t.Fatalf("%+v", f.Compactions)
	}
	if f.Start != 100_000 || f.Peak != 300_000 || f.Now != 151_000 {
		t.Fatalf("%+v", f)
	}
}

// Tools loaded while working count once, apart from those it started with.
func TestToolsLoadedOnDemandCountOnce(t *testing.T) {
	f := fillOf(t,
		shape("y", strings.Repeat("t", 1000)),
		answered("m1", 10_000, 0, 0, ""),
		`{"type":"attachment","attachment":{"type":"deferred_tools_record","entries":[{"d":"`+strings.Repeat("t", 1000)+`"}]}}`,
		shape("y", strings.Repeat("t", 2000)),
		shape("y", "t"), // a side request with fewer
		prompt(strings.Repeat("p", 1000)),
		answered("m2", 12_000, 0, 0, ""),
	)
	near(t, "loaded", tokens(f.Grown, PieceLoaded), 1000)
	near(t, "prompts", tokens(f.Grown, "prompts"), 1000)
	if tokens(f.Grown, PieceTools) != 0 {
		t.Fatalf("%+v", f.Grown)
	}
}

func TestATranscriptWithoutARequestHasNoContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	appendTo(t, path, prompt("go"), "{broken")
	if _, err := ReadFill(path); err == nil {
		t.Fatal("no request, no figures")
	}
	if _, err := ReadFill(path + "x"); err == nil {
		t.Fatal("no file")
	}
}

// Sessions are found by the directory they started in, or all of them.
func TestClaudeSessionsByDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	add := func(dir, name string, age time.Duration) string {
		d := filepath.Join(home, ".claude", "projects", dir)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(d, name+".jsonl")
		appendTo(t, p, prompt("go"))
		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
		return p
	}
	api := add("C--code-api", "a", time.Minute)
	old := add("C--code-api", "b", 48*time.Hour)
	web := add("-code-web-v2", "c", time.Hour)
	day := time.Now().Add(-24 * time.Hour)
	for _, c := range []struct {
		dirs  []string
		since time.Time
		want  []string
	}{
		{[]string{`c:\code\api`}, day, []string{api}},
		{[]string{`C:\code\api`}, time.Time{}, []string{api, old}},
		{[]string{"/code/web.v2"}, day, []string{web}},
		{[]string{"/code"}, day, nil},
		{nil, day, []string{api, web}},
	} {
		if got := ClaudeSessions(c.dirs, c.since); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%v: %v, want %v", c.dirs, got, c.want)
		}
	}
}

// What a result puts in the window is its text, and an image's worth for an
// image, however long its data; two hooks' output in one entry is each one's.
func TestResultsInBlocksAndSharedHookOutput(t *testing.T) {
	f := fillOf(t,
		answered("m1", 10_000, 0, 0, call("c1", "Read", "a.png")+","+call("c2", "mcp__web__get", "u")),
		`{"type":"user","message":{"content":[{"type":"text","text":"`+strings.Repeat("p", 1000)+`"},{"type":"tool_result","tool_use_id":"c1","content":[{"type":"image","source":{"data":"`+strings.Repeat("A", 500_000)+`"}}]},{"type":"tool_result","tool_use_id":"c2","content":[{"type":"text","text":"`+strings.Repeat("w", 2000)+`"}]},{"type":"tool_result","tool_use_id":"c?","content":"`+strings.Repeat("o", 1000)+`"}]}}`,
		`{"type":"attachment","attachment":{"type":"hook_additional_context","content":["[ainterlinked] `+strings.Repeat("m", 985)+`","`+strings.Repeat("h", 1000)+`"]},"rendered":[{"content":"`+strings.Repeat("r", 2000)+`"}]}`,
		answered("m2", 20_000, 0, 0, ""),
	)
	for name, want := range map[string]int64{"prompts": 1000, "Read results": 4000, "MCP web": 2000, "other tools results": 1000, PieceAinterlinked: 1000, "hooks": 1000} {
		near(t, name, tokens(f.Grown, name), want)
	}
}
