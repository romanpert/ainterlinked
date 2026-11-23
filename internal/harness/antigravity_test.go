package harness

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

func TestAntigravityInstallAndUninstall(t *testing.T) {
	e := sandbox(t)
	mcp := put(t, e, `{"mcpServers": {"unity": {"serverUrl": "http://127.0.0.1:8080/mcp"}}}`, ".gemini", "config", "mcp_config.json")
	hooks := put(t, e, `{"lint": {"PostToolUse": [{"matcher": "run_command", "hooks": [{"command": "./lint.sh"}]}]}}`, ".gemini", "config", "hooks.json")
	origMCP, origHooks := semantic(t, mcp, read(mcp)), semantic(t, hooks, read(hooks))
	commit(t, "antigravity", e, true)
	if w := ByID("antigravity").Wired(e); !w.MCP || !w.Hooks {
		t.Fatalf("wiring: %+v", w)
	}
	src := read(hooks)
	if !strings.Contains(string(src), "./lint.sh") {
		t.Fatalf("the user's hook is gone:\n%s", src)
	}
	// A matcher for the tools that write; the other events take none.
	v, _ := jsonc.Get(src, Server, "PreToolUse", jsonc.Index(0), "matcher")
	var matcher string
	_ = json.Unmarshal(v, &matcher)
	re, err := regexp.Compile("^(?:" + matcher + ")$") // it may match all of the name or a part
	if err != nil || matcher == "" {
		t.Fatalf("PreToolUse: %v\n%s", err, src)
	}
	for tool, want := range map[string]bool{
		"write_file": true, "write_to_file": true, "replace_file_content": true, "multi_replace_file_content": true, "edit_file": true, "sed_file": true, "notebook_edit": true,
		"read_file": false, "view_file": false, "list_dir": false, "run_command": false, "call_mcp_tool": false, "grep_search": false,
	} {
		if re.MatchString(tool) != want {
			t.Errorf("%s: %v", tool, !want)
		}
	}
	if _, ok := jsonc.Get(src, Server, "Stop", jsonc.Index(0), "command"); !ok {
		t.Fatalf("Stop takes its handlers directly:\n%s", src)
	}
	commit(t, "antigravity", e, true)
	if string(read(hooks)) != string(src) {
		t.Fatal("second install changed the file")
	}
	commit(t, "antigravity", e, false)
	if !equalJSON(semantic(t, mcp, read(mcp)), origMCP) || !equalJSON(semantic(t, hooks, read(hooks)), origHooks) {
		t.Fatalf("not restored:\n%s\n%s", read(mcp), read(hooks))
	}
}

func TestAntigravityHooksSwitchedOffStayOff(t *testing.T) {
	e := sandbox(t)
	put(t, e, `{"ainterlinked": {"enabled": false}}`, ".gemini", "config", "hooks.json")
	commit(t, "antigravity", e, true)
	if w := ByID("antigravity").Wired(e); w.Hooks || !w.MCP {
		t.Fatalf("wiring: %+v", w)
	}
}
