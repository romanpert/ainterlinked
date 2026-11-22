package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox points every path interlinked touches at temp dirs, and empties
// PATH so no real harness CLI (claude, codex...) can be found or run: an
// install here must never reach the developer's own configuration.
func sandbox(t *testing.T) (home, userHome, project string) {
	t.Helper()
	home, userHome = t.TempDir(), t.TempDir()
	project = filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTERLINKED_HOME", home)
	t.Setenv("INTERLINKED_USER_HOME", userHome)
	t.Setenv("INTERLINKED_HUB", "")
	t.Setenv("INTERLINKED_TOKEN", "")
	t.Setenv("INTERLINKED_BIN", "/opt/interlinked/interlinked")
	t.Setenv("INTERLINKED_ANCHOR_PID", "4242")
	t.Setenv("INTERLINKED_NAME", "")
	t.Setenv("PATH", t.TempDir())
	t.Setenv("APPDATA", filepath.Join(userHome, "AppData", "Roaming"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(userHome, ".config"))
	wd, _ := os.Getwd()
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return home, userHome, project
}

// cli runs Main in-process with stdin, capturing stdout and stderr.
func cli(t *testing.T, stdin string, args ...string) (out, errOut string, code int) {
	t.Helper()
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdin, os.Stdout, os.Stderr = inR, outW, errW
	go func() { _, _ = io.WriteString(inW, stdin); inW.Close() }()
	var ob, eb bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(&ob, outR); done <- struct{}{} }()
	go func() { _, _ = io.Copy(&eb, errR); done <- struct{}{} }()
	code = Main(args)
	outW.Close()
	errW.Close()
	<-done
	<-done
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	inR.Close()
	return ob.String(), eb.String(), code
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestVersionHelpAndUnknownCommand(t *testing.T) {
	sandbox(t)
	if out, _, code := cli(t, "", "version"); code != 0 || !strings.Contains(out, "interlinked "+Version) {
		t.Fatalf("version: %d %q", code, out)
	}
	if out, _, code := cli(t, "", "help"); code != 0 || !strings.Contains(out, "install [harness...]") {
		t.Fatalf("help: %d %q", code, out)
	}
	if _, errOut, code := cli(t, "", "frobnicate"); code != 2 || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Fatalf("unknown: %d %q", code, errOut)
	}
}

func TestConfigIsCreatedWithDefaults(t *testing.T) {
	home, _, _ := sandbox(t)
	out, _, code := cli(t, "", "config", "--path")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), home) {
		t.Fatalf("config --path: %d %q (home %s)", code, out, home)
	}
	out, _, code = cli(t, "", "config")
	if code != 0 || !strings.Contains(out, "[delivery]") || !strings.Contains(out, "share_prompts = false") {
		t.Fatalf("config: %d %q", code, out)
	}
}

// Install, mute, reinstall and uninstall two file-configured harnesses in a
// sandboxed home, checking the user's own settings survive every step.
func TestInstallLifecycleInSandbox(t *testing.T) {
	_, userHome, _ := sandbox(t)
	geminiSettings := filepath.Join(userHome, ".gemini", "settings.json")
	if err := os.MkdirAll(filepath.Dir(geminiSettings), 0o700); err != nil {
		t.Fatal(err)
	}
	original := "{\n  // mine\n  \"theme\": \"Dracula\"\n}\n"
	if err := os.WriteFile(geminiSettings, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(userHome, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	cursorMCP := filepath.Join(userHome, ".cursor", "mcp.json")

	out, _, code := cli(t, "", "scan")
	if code != 0 || !strings.Contains(out, "HARNESS") {
		t.Fatalf("scan: %d %q", code, out)
	}

	out, errOut, code := cli(t, "", "install", "cursor", "gemini", "--dry-run")
	if code != 0 || !strings.Contains(out, "Dry run") {
		t.Fatalf("dry run: %d %q %q", code, out, errOut)
	}
	if _, err := os.Stat(cursorMCP); err == nil || mustRead(t, geminiSettings) != original {
		t.Fatal("dry run wrote files")
	}

	if out, errOut, code = cli(t, "", "install", "cursor", "gemini"); code != 0 {
		t.Fatalf("install: %d %q %q", code, out, errOut)
	}
	g := mustRead(t, geminiSettings)
	if !strings.Contains(g, "interlinked") || !strings.Contains(g, "// mine") || !strings.Contains(g, "Dracula") {
		t.Fatalf("gemini settings after install:\n%s", g)
	}
	if c := mustRead(t, cursorMCP); !strings.Contains(c, "/opt/interlinked/interlinked") {
		t.Fatalf("cursor mcp.json doesn't use the stable binary:\n%s", c)
	}
	if _, err := os.Stat(filepath.Join(userHome, ".cursor", "hooks.json")); err != nil {
		t.Fatal("cursor hooks.json missing:", err)
	}

	// Idempotent.
	before := mustRead(t, geminiSettings) + mustRead(t, cursorMCP)
	if _, _, code = cli(t, "", "install", "cursor", "gemini"); code != 0 {
		t.Fatal("second install failed")
	}
	if after := mustRead(t, geminiSettings) + mustRead(t, cursorMCP); after != before {
		t.Fatalf("second install changed files:\n%s\n---\n%s", before, after)
	}

	out, _, code = cli(t, "", "scan")
	if code != 0 || !regexpLine(out, "cursor", "yes", "on") {
		t.Fatalf("scan after install:\n%s", out)
	}
	if _, _, code = cli(t, "", "disable", "cursor"); code != 0 {
		t.Fatal("disable failed")
	}
	if out, _, _ = cli(t, "", "scan"); !regexpLine(out, "cursor", "muted") {
		t.Fatalf("cursor should be muted:\n%s", out)
	}
	if _, _, code = cli(t, "", "enable", "cursor"); code != 0 {
		t.Fatal("enable failed")
	}
	if _, errOut, code = cli(t, "", "enable", "nonsense"); code != 1 || !strings.Contains(errOut, "unknown harness") {
		t.Fatalf("enable unknown: %d %q", code, errOut)
	}

	if out, errOut, code = cli(t, "", "uninstall", "cursor", "gemini"); code != 0 {
		t.Fatalf("uninstall: %d %q %q", code, out, errOut)
	}
	g = mustRead(t, geminiSettings)
	if strings.Contains(g, "interlinked") || !strings.Contains(g, "// mine") || !strings.Contains(g, "Dracula") {
		t.Fatalf("gemini settings after uninstall:\n%s", g)
	}
	if c, err := os.ReadFile(cursorMCP); err == nil && strings.Contains(string(c), "interlinked") {
		t.Fatalf("cursor still wired:\n%s", c)
	}
	if _, errOut, code = cli(t, "", "install", "nonsense"); code != 1 || !strings.Contains(errOut, "unknown harness") {
		t.Fatalf("install unknown: %d %q", code, errOut)
	}
}

// regexpLine reports whether some line starts with name and contains every word.
func regexpLine(out, name string, words ...string) bool {
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, name+" ") {
			continue
		}
		f := strings.Fields(l)
		ok := true
		for _, w := range words {
			found := false
			for _, x := range f {
				found = found || x == w
			}
			ok = ok && found
		}
		if ok {
			return true
		}
	}
	return false
}

// The operator's commands against a real hub, with one agent joined through
// its hooks (the anchor pid is pinned so hook calls are one session).
func TestOperatorCommandsAgainstALiveAgent(t *testing.T) {
	_, _, project := sandbox(t)
	in := func(extra string) string {
		return `{"session_id":"s1","cwd":` + quoteJSON(project) + extra + `}`
	}
	out, errOut, code := cli(t, in(""), "hook", "claude-code", "SessionStart")
	if code != 0 || !strings.Contains(out, `You are \"claude@proj\"`) {
		t.Fatalf("SessionStart hook: %d %q %q", code, out, errOut)
	}
	if out, _, _ = cli(t, "", "agents"); !strings.Contains(out, "claude@proj") {
		t.Fatalf("agents: %q", out)
	}
	if out, errOut, code = cli(t, "", "send", "claude@proj", "please", "rebase"); code != 0 || !strings.Contains(out, "sent #") {
		t.Fatalf("send: %d %q %q", code, out, errOut)
	}
	if _, errOut, code = cli(t, "", "send", "only-a-target"); code != 1 || !strings.Contains(errOut, "usage") {
		t.Fatalf("send usage: %d %q", code, errOut)
	}
	out, _, _ = cli(t, in(`,"tool_name":"Bash","tool_input":{"command":"ls"}`), "hook", "claude-code", "PostToolUse")
	if !strings.Contains(out, "please rebase") {
		t.Fatalf("message not injected after a tool call: %q", out)
	}

	if out, errOut, code = cli(t, "", "task", "add", "write", "schema", "--board", "web", "--prio", "1", "--label", "db"); code != 0 || !strings.Contains(out, "created") {
		t.Fatalf("task add: %d %q %q", code, out, errOut)
	}
	if out, _, _ = cli(t, "", "tasks", "--board", "web"); !strings.Contains(out, "write schema") {
		t.Fatalf("tasks: %q", out)
	}
	if out, errOut, code = cli(t, "", "task", "claim", "#1"); code != 0 {
		t.Fatalf("task claim: %d %q %q", code, out, errOut)
	}
	if out, errOut, code = cli(t, "", "task", "done", "1", "--result", "shipped"); code != 0 || !strings.Contains(out, "done") {
		t.Fatalf("task done: %d %q %q", code, out, errOut)
	}
	if _, errOut, code = cli(t, "", "task", "explode", "1"); code != 1 || !strings.Contains(errOut, "unknown task command") {
		t.Fatalf("bad task command: %d %q", code, errOut)
	}
	if _, errOut, code = cli(t, "", "task", "done", "abc"); code != 1 || !strings.Contains(errOut, "bad task id") {
		t.Fatalf("bad task id: %d %q", code, errOut)
	}
	if _, _, code = cli(t, "", "locks"); code != 0 {
		t.Fatal("locks failed")
	}
	if _, _, code = cli(t, "", "inbox"); code != 0 {
		t.Fatal("inbox failed")
	}
	if out, _, code = cli(t, "", "feed", "-n", "20"); code != 0 || !strings.Contains(out, "joined") {
		t.Fatalf("feed: %d %q", code, out)
	}
	if out, _, code = cli(t, ""); code != 0 || !strings.Contains(out, "claude@proj") {
		t.Fatalf("overview: %d %q", code, out)
	}

	// SessionEnd marks the agent leaving (offline unless it checks in again soon).
	cli(t, in(""), "hook", "claude-code", "SessionEnd")
	if out, _, _ = cli(t, "", "agents"); !strings.Contains(out, "claude@proj [claude-code] leaving") {
		t.Fatalf("agent should be leaving after SessionEnd: %q", out)
	}
}

func quoteJSON(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
