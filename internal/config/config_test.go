package config

import (
	"os"
	"strings"
	"testing"
)

func TestSaveLoadAndEnvNotPersisted(t *testing.T) {
	t.Setenv("INTERLINKED_HOME", t.TempDir())
	cfg := Default()
	cfg.Harnesses["codex"] = HarnessCfg{Enabled: false}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INTERLINKED_HUB", "http://elsewhere:7717")
	t.Setenv("INTERLINKED_TOKEN", "secret")
	got, err := Load()
	if err != nil || got.Hub != "http://elsewhere:7717" || got.HarnessEnabled("codex") {
		t.Fatalf("load: %+v %v", got, err)
	}
	file, _ := LoadFile()
	if file.Hub != "" || file.Token != "" {
		t.Fatal("environment overrides must not leak into what gets saved")
	}
	b, _ := os.ReadFile(Path())
	if strings.Contains(string(b), "secret") {
		t.Fatal("token from the environment was written to disk")
	}
}

func TestBrokenConfigIsAnError(t *testing.T) {
	t.Setenv("INTERLINKED_HOME", t.TempDir())
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
