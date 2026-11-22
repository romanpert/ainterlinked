package hooks

import "testing"

// Grok drops what SessionStart and UserPromptSubmit print, and nothing else:
// a refused edit or a held stop reaches it even when mail rides along.
func TestGrokNeverLosesADenyOrABlock(t *testing.T) {
	h, _ := grok("PreToolUse", PreTool, Result{Deny: "src/ is reserved", Context: "mail"})["hookSpecificOutput"].(map[string]any)
	if h["permissionDecision"] != "deny" {
		t.Fatalf("deny with context dropped: %v", h)
	}
	if out := grok("Stop", Stop, Result{Block: "keep going", Context: "mail"}); out["decision"] != "block" {
		t.Fatalf("block with context dropped: %v", out)
	}
	for _, k := range []Kind{Start, Prompt} {
		if out := grok("x", k, Result{Context: "mail"}); out != nil {
			t.Fatalf("%s: Grok discards this output, so none is written: %v", k, out)
		}
	}
}
