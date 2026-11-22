package harness

import (
	"strings"
	"testing"
)

// Waking idle sessions costs the user turns: the hook is there only when
// they asked, and gone when they change their mind.
func TestClaudeWaiterIsWiredOnlyWhenAskedFor(t *testing.T) {
	e := sandbox(t)
	settings := func() string { return string(read(e.path(".claude", "settings.json"))) }
	commit(t, "claude-code", e, true)
	if strings.Contains(settings(), "asyncRewake") {
		t.Fatalf("wired by default:\n%s", settings())
	}
	e.WakeIdle = true
	commit(t, "claude-code", e, true)
	on := settings()
	if strings.Count(on, `"asyncRewake": true`) != 1 || strings.Count(on, "hook claude-code Wake") != 1 || strings.Count(on, "hook claude-code Stop") != 1 {
		t.Fatalf("settings:\n%s", on)
	}
	commit(t, "claude-code", e, true)
	if settings() != on {
		t.Fatal("second install changed the file")
	}
	e.WakeIdle = false
	commit(t, "claude-code", e, true)
	if strings.Contains(settings(), "Wake") {
		t.Fatalf("still wired:\n%s", settings())
	}
	e.WakeIdle = true
	commit(t, "claude-code", e, true)
	commit(t, "claude-code", e, false)
	if strings.Contains(settings(), "hook claude-code") {
		t.Fatalf("uninstall left it:\n%s", settings())
	}
}
