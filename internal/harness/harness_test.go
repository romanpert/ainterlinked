package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/romanpert/interlinked/internal/jsonc"
)

var fixtures = map[string]string{
	".claude.json": `{"numStartups": 3, "mcpServers": {"github": {"type": "http", "url": "https://x"}}, "projects": {}}`,
	".claude/settings.json": `{
  "model": "opus",
  "permissions": { "allow": ["Bash(ls:*)"] },
  "hooks": {
    "PostToolUse": [
      { "matcher": "Edit", "hooks": [ { "type": "command", "command": "code-review-graph update" } ] }
    ]
  }
}`,
	".codex/config.toml":                 "model = \"gpt-5\"\r\n\r\n[mcp_servers.gitnexus]\r\ncommand = \"gitnexus\"\r\nargs = [\"mcp\"]\r\n\r\n[features]\r\nrmcp_client = true\r\n",
	".copilot/mcp-config.json":           `{"mcpServers": {"unityMCP": {"url": "http://127.0.0.1:8080/mcp", "type": "http"}}}`,
	".cursor/mcp.json":                   `{"mcpServers": {"weather": {"command": "w"}}}`,
	".gemini/settings.json":              `{"hooks": {"AfterTool": [{"hooks": [{"type": "command", "command": "other"}]}]}, "mcpServers": {}}`,
	".qwen/settings.json":                `{"mcpServers": {"unityMCP": {"command": "u"}}}`,
	".config/opencode/opencode.json":     `{"$schema": "https://opencode.ai/config.json", "mcp": {"gitnexus": {"type": "local", "command": ["gitnexus", "mcp"]}}}`,
	".codeium/windsurf/mcp_config.json":  `{"mcpServers": {"mcp-aemps": {"command": "uvx"}}}`,
	".kiro/settings/mcp.json":            `{"mcpServers": {}}`,
	"AppData/Roaming/Zed/settings.json":  "// Zed settings\n{\n  \"theme\": \"One\", // mine\n  \"context_servers\": {\n    \"mcp-aemps\": { \"command\": \"uvx\" }\n  }\n}\n",
	"AppData/Roaming/Code/User/mcp.json": "{\n\t\"servers\": {\n\t\t\"MCP_DOCKER\": { \"command\": \"docker\" }\n\t},\n\t\"inputs\": []\n}",
}

func sandbox(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	for p, c := range fixtures {
		full := filepath.Join(home, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e := NewEnv(home, `C:\Tools\interlinked.exe`)
	e.GOOS = "windows"
	e.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	return e
}

func apply(t *testing.T, e *Env, install bool) int {
	t.Helper()
	n := 0
	for _, h := range All() {
		tx := NewTx(false, io.Discard)
		if install {
			if len(h.Detect(e)) == 0 {
				continue
			}
			h.Install(e, tx)
		} else {
			h.Uninstall(e, tx)
		}
		ch, err := tx.Commit()
		if err != nil {
			t.Fatalf("%s: %v", h.ID, err)
		}
		n += len(ch)
	}
	return n
}

func semantic(t *testing.T, path string, src []byte) any {
	t.Helper()
	if strings.HasSuffix(path, ".toml") {
		return strings.TrimRight(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	}
	var v any
	if err := json.Unmarshal(jsonc.Strip(src), &v); err != nil {
		t.Fatalf("%s invalid: %v\n%s", path, err, src)
	}
	return v
}

func TestInstallIdempotentAndUninstallRestores(t *testing.T) {
	e := sandbox(t)
	if n := apply(t, e, true); n == 0 {
		t.Fatal("install changed nothing")
	}
	// Every wired harness reports itself wired; hooks where we have them.
	for _, h := range All() {
		if len(h.Detect(e)) == 0 {
			continue
		}
		w := h.Wired(e)
		if !w.MCP || (h.Hooks && !w.Hooks) {
			t.Errorf("%s not wired after install: %+v", h.ID, w)
		}
	}
	// Spot checks on content.
	settings := read(e.path(".claude", "settings.json"))
	if !bytes.Contains(settings, []byte(`"command": "C:/Tools/interlinked.exe hook claude-code Stop"`)) ||
		!bytes.Contains(settings, []byte("code-review-graph update")) || !bytes.Contains(settings, []byte(`"mcp__interlinked"`)) {
		t.Errorf("claude settings:\n%s", settings)
	}
	toml := string(read(e.path(".codex", "config.toml")))
	if !strings.Contains(toml, "[mcp_servers.interlinked]\r\ncommand = 'C:\\Tools\\interlinked.exe'") || !strings.Contains(toml, "[mcp_servers.gitnexus]") {
		t.Errorf("codex toml:\n%s", toml)
	}
	zedSrc := read(e.appData("Zed", "settings.json"))
	if !bytes.Contains(zedSrc, []byte("// mine")) || !bytes.Contains(zedSrc, []byte(`"interlinked"`)) {
		t.Errorf("zed comments must survive:\n%s", zedSrc)
	}
	if !exists(e.path(".config", "opencode", "plugins", "interlinked.js")) || !exists(copilotHooksFile(e)) {
		t.Error("owned files missing")
	}
	if plugin := read(e.path(".config", "opencode", "plugins", "interlinked.js")); !bytes.Contains(plugin, []byte(`"C:/Tools/interlinked.exe"`)) {
		t.Error("plugin binary path not substituted")
	}

	// Second install: nothing to do.
	if n := apply(t, e, true); n != 0 {
		t.Fatalf("install is not idempotent: %d changes", n)
	}

	apply(t, e, false)
	for p, orig := range fixtures {
		full := filepath.Join(e.Home, filepath.FromSlash(p))
		got := read(full)
		if !reflect.DeepEqual(semantic(t, p, got), semantic(t, p, []byte(orig))) {
			t.Errorf("%s not restored by uninstall:\n--- got\n%s\n--- want\n%s", p, got, orig)
		}
	}
	if exists(e.path(".config", "opencode", "plugins", "interlinked.js")) || exists(copilotHooksFile(e)) {
		t.Error("owned files should be removed")
	}
	// Backups were made of every edited file.
	backups, _ := filepath.Glob(filepath.Join(e.Home, ".claude", "settings.json.bak-interlinked-*"))
	if len(backups) == 0 {
		t.Error("no backup of settings.json")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := sandbox(t)
	before := read(e.path(".claude", "settings.json"))
	var out bytes.Buffer
	tx := NewTx(true, &out)
	ByID("claude-code").Install(e, tx)
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, read(e.path(".claude", "settings.json"))) {
		t.Fatal("dry run wrote to disk")
	}
	if !strings.Contains(out.String(), "would modify") || !strings.Contains(out.String(), "interlinked.exe hook claude-code SessionStart") {
		t.Fatalf("dry run output:\n%s", out.String())
	}
}

func TestBrokenConfigIsLeftAlone(t *testing.T) {
	e := sandbox(t)
	p := e.path(".cursor", "mcp.json")
	if err := os.WriteFile(p, []byte(`{"mcpServers": {`), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTx(false, io.Discard)
	ByID("cursor").Install(e, tx)
	if _, err := tx.Commit(); err == nil {
		t.Fatal("expected an error for a broken file")
	}
	if string(read(p)) != `{"mcpServers": {` {
		t.Fatal("broken file must not be touched")
	}
	if exists(e.path(".cursor", "hooks.json")) {
		t.Fatal("a failing edit must leave the whole harness untouched")
	}
}

func TestUserHookSharingOurGroupSurvives(t *testing.T) {
	e := sandbox(t)
	p := e.path(".claude", "settings.json")
	mixed := `{"hooks": {"Stop": [{"hooks": [
    {"type": "command", "command": "my-linter --fix"},
    {"type": "command", "command": "C:/Old/Path/interlinked.exe hook claude-code Stop"}
  ]}, {"hooks": [{"type": "command", "command": "node ~/src/interlinked/scripts/x.mjs hook"}]}]}}`
	if err := os.WriteFile(p, []byte(mixed), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTx(false, io.Discard)
	ByID("claude-code").Uninstall(e, tx)
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := string(read(p))
	if !strings.Contains(got, "my-linter --fix") || !strings.Contains(got, "scripts/x.mjs hook") {
		t.Fatalf("user hooks deleted:\n%s", got)
	}
	if strings.Contains(got, "Old/Path/interlinked.exe") {
		t.Fatalf("our old entry should be gone:\n%s", got)
	}
	semantic(t, p, []byte(got))
}

func TestTOMLKeepsNextTablesComment(t *testing.T) {
	src := "a = 1\n\n[mcp_servers.interlinked]\ncommand = 'x'\n\n# my features\n[features]\nb = true\n"
	out, ok := tomlRemoveTable(src, "mcp_servers.interlinked")
	if !ok || !strings.Contains(out, "# my features\n[features]") || strings.Contains(out, "interlinked") {
		t.Fatalf("got:\n%s", out)
	}
}

func TestCRLFStaysCRLF(t *testing.T) {
	e := sandbox(t)
	p := e.path(".cursor", "mcp.json")
	if err := os.WriteFile(p, []byte("{\r\n  \"mcpServers\": {\r\n    \"w\": { \"command\": \"w\" }\r\n  }\r\n}\r\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tx := NewTx(false, io.Discard)
	ByID("cursor").Install(e, tx)
	if _, err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	b := read(p)
	if bytes.Count(b, []byte("\n")) != bytes.Count(b, []byte("\r\n")) {
		t.Fatalf("mixed line endings:\n%q", b)
	}
}

func TestCommandQuoting(t *testing.T) {
	e := &Env{Bin: `C:\Program Files\il\interlinked.exe`}
	if got := e.hookCmd("cursor", "stop", true); got != "& 'C:/Program Files/il/interlinked.exe' hook cursor stop" {
		t.Errorf("powershell: %s", got)
	}
	if got := e.hookCmd("claude-code", "Stop", false); got != `"C:/Program Files/il/interlinked.exe" hook claude-code Stop` {
		t.Errorf("sh: %s", got)
	}
}
