package jsonc

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustValid(t *testing.T, src []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(Strip(src), &v); err != nil {
		t.Fatalf("result is not valid JSON(C): %v\n%s", err, src)
	}
	return v
}

func TestSetPreservesCommentsAndOrder(t *testing.T) {
	src := []byte(`// Zed settings
{
  "theme": "One Dark", // keep me
  /* block */
  "context_servers": {
    "other": { "command": "x" }
  },
}
`)
	out, err := Set(src, []string{"context_servers", "interlinked"}, map[string]any{"command": "interlinked", "args": []string{"mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"// Zed settings", "// keep me", "/* block */", `"other": { "command": "x" }`, `"interlinked": {`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	if strings.Index(s, `"theme"`) > strings.Index(s, `"context_servers"`) {
		t.Error("key order changed")
	}
	v := mustValid(t, out)
	cs := v["context_servers"].(map[string]any)
	if cs["interlinked"].(map[string]any)["command"] != "interlinked" {
		t.Fatalf("not set: %v", cs)
	}
	// Idempotent replace.
	out2, err := Set(out, []string{"context_servers", "interlinked"}, map[string]any{"command": "il2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out2), `"interlinked"`) != 1 || !strings.Contains(string(out2), "il2") {
		t.Fatalf("replace failed:\n%s", out2)
	}
	mustValid(t, out2)
	// Delete restores the original servers.
	out3, ok, err := Delete(out2, []string{"context_servers", "interlinked"})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	v3 := mustValid(t, out3)
	if _, has := v3["context_servers"].(map[string]any)["interlinked"]; has {
		t.Fatal("not deleted")
	}
	if !strings.Contains(string(out3), "// keep me") {
		t.Fatal("comment lost on delete")
	}
}

func TestSetCreatesPathsAndEmptyFiles(t *testing.T) {
	for _, src := range []string{"", "{}", "{\n}\n", `{"a":1}`} {
		out, err := Set([]byte(src), []string{"mcpServers", "interlinked"}, map[string]any{"command": "il"})
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		v := mustValid(t, out)
		if v["mcpServers"].(map[string]any)["interlinked"].(map[string]any)["command"] != "il" {
			t.Fatalf("%q -> %s", src, out)
		}
		if src == `{"a":1}` && v["a"].(float64) != 1 {
			t.Fatal("lost a")
		}
	}
}

func TestAppendAndRemoveIf(t *testing.T) {
	src := []byte(`{
  "hooks": {
    "Stop": [
      { "hooks": [ { "type": "command", "command": "other-tool" } ] }
    ]
  }
}`)
	entry := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "interlinked hook claude-code Stop"}}}
	out, err := Append(src, []string{"hooks", "Stop"}, entry)
	if err != nil {
		t.Fatal(err)
	}
	out, err = Append(out, []string{"hooks", "SessionStart"}, entry)
	if err != nil {
		t.Fatal(err)
	}
	v := mustValid(t, out)
	if n := len(v["hooks"].(map[string]any)["Stop"].([]any)); n != 2 {
		t.Fatalf("want 2 Stop entries, got %d\n%s", n, out)
	}
	pred := func(raw []byte) bool { return strings.Contains(string(raw), "interlinked hook") }
	out, n, err := RemoveIf(out, []string{"hooks", "Stop"}, pred)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	out, n, err = RemoveIf(out, []string{"hooks", "SessionStart"}, pred)
	if err != nil || n != 1 {
		t.Fatal(n, err)
	}
	v = mustValid(t, out)
	stop := v["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 1 || !strings.Contains(string(out), "other-tool") {
		t.Fatalf("other hook damaged:\n%s", out)
	}
	if Len(out, "hooks", "SessionStart") != 0 {
		t.Fatalf("SessionStart should be empty:\n%s", out)
	}
}

func TestInlineObject(t *testing.T) {
	out, err := Set([]byte(`{"mcp": {"a": {"type": "local"}}}`), []string{"mcp", "b"}, map[string]any{"type": "local"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "\n") {
		t.Fatalf("inline object should stay inline: %s", out)
	}
	mustValid(t, out)
}

func TestDeleteKeepsNeighbourComments(t *testing.T) {
	src := []byte(`{
  "servers": {
    "interlinked": { "command": "il" },
    // my github server
    "github": { "url": "x" }, // keep this note
    "last": 1
  }
}`)
	out, ok, err := Delete(src, []string{"servers", "interlinked"})
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if !strings.Contains(string(out), "// my github server") {
		t.Fatalf("comment above the next key lost:\n%s", out)
	}
	out, _, _ = Delete(out, []string{"servers", "last"})
	if !strings.Contains(string(out), "// keep this note") {
		t.Fatalf("previous member's trailing comment lost:\n%s", out)
	}
	mustValid(t, out)
}

func TestInsertIntoCommentedEmptyObject(t *testing.T) {
	out, err := Set([]byte("{\n  \"servers\": { // add servers here\n  }\n}"), []string{"servers", "a"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "// add servers here") {
		t.Fatalf("comment wiped:\n%s", out)
	}
	mustValid(t, out)
}

func TestTrailingCommaBeforeComment(t *testing.T) {
	src := []byte("{\"allow\": [\"Bash\", // why\n]}")
	var v struct{ Allow []string }
	if _, err := Unmarshal(src, &v); err != nil || len(v.Allow) != 1 {
		t.Fatalf("trailing comma before a comment: %v %v", err, v)
	}
}

func TestValidIsStrict(t *testing.T) {
	for _, bad := range []string{`{"a": tru}`, `{"a": 1 "b": 2}`} {
		if Valid([]byte(bad)) == nil {
			t.Errorf("%s should be invalid", bad)
		}
	}
}

func TestRejectsGarbage(t *testing.T) {
	if _, err := Set([]byte(`{"a": `), []string{"b"}, 1); err == nil {
		t.Fatal("expected error")
	}
}
