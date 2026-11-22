package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveLoad(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	cfg := Default()
	cfg.Harnesses["codex"] = HarnessCfg{Enabled: false}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.HarnessEnabled("codex") || got.OldHub != "" {
		t.Fatalf("load: %+v %v", got, err)
	}
}

// A hub URL (and token) from a version that joined other machines is
// noticed, not used, and gone after the next save.
func TestOldRemoteHubIsIgnored(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	if err := os.WriteFile(Path(), []byte("hub = \"http://elsewhere:7717\"\ntoken = \"secret\"\nmachine = \"box\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil || got.OldHub != "http://elsewhere:7717" || got.Machine != "box" {
		t.Fatalf("load: %+v %v", got, err)
	}
	if err := Save(got); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "elsewhere") || strings.Contains(string(b), "secret") {
		t.Fatalf("saving drops the old hub and token:\n%s", b)
	}
}

func TestBrokenConfigIsAnError(t *testing.T) {
	t.Setenv("AINTERLINKED_HOME", t.TempDir())
	if err := os.WriteFile(Path(), []byte("hub = \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(); err == nil {
		t.Fatal("a broken config must be reported, not silently replaced")
	}
}

func TestCopilotHooksFollowBothHarnesses(t *testing.T) {
	c := Default()
	if !c.HarnessEnabled("copilot") {
		t.Fatal("enabled by default")
	}
	c.Harnesses["copilot-cli"] = HarnessCfg{Enabled: false}
	if !c.HarnessEnabled("copilot") {
		t.Fatal("VS Code still uses the shared hooks")
	}
	c.Harnesses["vscode"] = HarnessCfg{Enabled: false}
	if c.HarnessEnabled("copilot") {
		t.Fatal("both muted → shared hooks muted")
	}
}

// A machine that only has the pre-rename ~/.interlinked keeps its one hub
// there; a fresh machine, or one already renamed, uses ~/.ainterlinked.
func TestHomeFollowsTheExistingDirectory(t *testing.T) {
	user := t.TempDir()
	t.Setenv("AINTERLINKED_HOME", "")
	t.Setenv("AINTERLINKED_USER_HOME", user)
	if got := Home(); got != filepath.Join(user, ".ainterlinked") || LegacyHome() {
		t.Fatalf("fresh: %s legacy=%v", got, LegacyHome())
	}
	if err := os.Mkdir(filepath.Join(user, ".interlinked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Home(); got != filepath.Join(user, ".interlinked") || !LegacyHome() {
		t.Fatalf("legacy only: %s legacy=%v", got, LegacyHome())
	}
	if err := os.Mkdir(filepath.Join(user, ".ainterlinked"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := Home(); got != filepath.Join(user, ".ainterlinked") || LegacyHome() {
		t.Fatalf("renamed: %s legacy=%v", got, LegacyHome())
	}
}
