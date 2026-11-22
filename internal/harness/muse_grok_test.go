package harness

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

func commit(t *testing.T, id string, e *Env, install bool) {
	t.Helper()
	tx := NewTx(false, io.Discard)
	if install {
		ByID(id).Install(e, tx)
	} else {
		ByID(id).Uninstall(e, tx)
	}
	if _, err := tx.Commit(); err != nil {
		t.Fatalf("%s: %v", id, err)
	}
}

func put(t *testing.T, e *Env, content string, parts ...string) string {
	t.Helper()
	p := e.path(parts...)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Muse refuses to start on a settings file without schema_version.
func TestMuseNewSettingsFileHasSchemaVersion(t *testing.T) {
	e := sandbox(t)
	commit(t, "muse", e, true)
	src := read(e.path(".config", "muse", "settings.json"))
	if v, ok := jsonc.Get(src, "schema_version"); !ok || string(v) != "1" {
		t.Fatalf("schema_version missing:\n%s", src)
	}
	if w := ByID("muse").Wired(e); !w.MCP || !w.Hooks {
		t.Fatalf("wiring: %+v", w)
	}
	if !strings.Contains(string(src), "hook muse Stop") {
		t.Fatalf("hooks:\n%s", src)
	}
	// Muse skips a group whose matcher doesn't apply to its event.
	if _, ok := jsonc.Get(src, "hooks", "Stop", jsonc.Index(0), "matcher"); ok {
		t.Fatalf("Stop takes no matcher:\n%s", src)
	}
}

// Both spellings in one file disable MCP for the whole session.
func TestMuseKeepsToTheUsersServerSpelling(t *testing.T) {
	e := sandbox(t)
	orig := `{"schema_version": 1, "mcp_servers": {"docs": {"transport": "stdio", "command": "d"}}}`
	p := put(t, e, orig, ".config", "muse", "settings.json")
	commit(t, "muse", e, true)
	src := read(p)
	if _, ok := jsonc.Get(src, "mcpServers"); ok {
		t.Fatalf("mixed spellings:\n%s", src)
	}
	if _, ok := jsonc.Get(src, "mcp_servers", Server); !ok {
		t.Fatalf("not wired:\n%s", src)
	}
	commit(t, "muse", e, true)
	if string(read(p)) != string(src) {
		t.Fatal("second install changed the file")
	}
	commit(t, "muse", e, false)
	if got := semantic(t, p, read(p)); !equalJSON(got, semantic(t, p, []byte(orig))) {
		t.Fatalf("not restored:\n%s", read(p))
	}
}

func TestGrokInstallAndUninstall(t *testing.T) {
	e := sandbox(t)
	orig := "[mcp_servers.linear]\nurl = \"https://mcp.linear.app/mcp\"\n"
	p := put(t, e, orig, ".grok", "config.toml")
	commit(t, "grok", e, true)
	if w := ByID("grok").Wired(e); !w.MCP || !w.Hooks {
		t.Fatalf("wiring: %+v", w)
	}
	toml := string(read(p))
	if !strings.Contains(toml, "[mcp_servers.linear]") || !strings.Contains(toml, `"--harness", "grok"`) {
		t.Fatalf("toml:\n%s", toml)
	}
	// Grok's default hook timeout is 5 s: every entry must carry its own.
	hooks := string(read(e.path(".grok", "hooks", Server+".json")))
	if strings.Count(hooks, `"timeout": 15`) != len(grokHooks) {
		t.Fatalf("timeouts:\n%s", hooks)
	}
	commit(t, "grok", e, false)
	if exists(e.path(".grok", "hooks", Server+".json")) || strings.Contains(string(read(p)), Server) {
		t.Fatal("uninstall left something behind")
	}
	if strings.TrimSpace(string(read(p))) != strings.TrimSpace(orig) {
		t.Fatalf("config.toml not restored:\n%s", read(p))
	}
}

func equalJSON(a, b any) bool {
	x, _ := jsonMarshal(a)
	y, _ := jsonMarshal(b)
	return x == y
}
