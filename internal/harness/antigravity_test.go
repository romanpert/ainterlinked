package harness

import (
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
	if v, ok := jsonc.Get(src, Server, "PreToolUse", jsonc.Index(0), "matcher"); !ok || !strings.Contains(string(v), "write_to_file|replace_file_content|") {
		t.Fatalf("PreToolUse:\n%s", src)
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
