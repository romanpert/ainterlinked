package hooks

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/proc"
)

// anchorName is the program this hook runs inside (tests replace it).
var anchorName = func() string { _, name := proc.Anchor(); return name }

// runsAs says which harness a hook acts for, and whether it runs at all.
// Grok Build also runs the hooks in Claude Code's and Cursor's files, where
// it would register as them: they stay silent there when Grok's own hook
// file does their work, and act as Grok's when it doesn't, so a reserved
// file is refused either way. The variable only says when to look; the
// process that ran the hook decides.
func runsAs(harness string) (as string, run bool) {
	if harness == "grok" || os.Getenv("GROK_HOOK_EVENT") == "" || anchorName() != "grok" {
		return harness, true
	}
	b, _ := os.ReadFile(filepath.Join(config.UserHome(), ".grok", "hooks", "ainterlinked.json"))
	return "grok", !strings.Contains(string(b), " hook grok ")
}
