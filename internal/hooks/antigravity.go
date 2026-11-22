package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/romanpert/ainterlinked/internal/config"
)

// parseAntigravity reads what Antigravity names its own way.
func parseAntigravity(m map[string]any, in *Input) {
	if in.SessionID == "" {
		in.SessionID = str(m, "conversationId")
	}
	if roots, ok := m["workspacePaths"].([]any); ok && len(roots) > 0 && in.Cwd == "" {
		in.Cwd, _ = roots[0].(string)
	}
	if in.Model == "" {
		in.Model = str(m, "modelName")
	}
	if in.Prompt == "" {
		in.Prompt = str(m, "lastUserInput")
	}
	call, ok := m["toolCall"].(map[string]any)
	if !ok {
		return
	}
	in.ToolName = str(call, "name")
	args, _ := call["args"].(map[string]any)
	in.ToolInput = map[string]any{}
	for k, v := range args {
		in.ToolInput[k] = v
	}
	for ours, theirs := range map[string][]string{"file_path": {"TargetFile", "AbsolutePath"}, "command": {"CommandLine"}} {
		if v := str(args, theirs...); v != "" {
			in.ToolInput[ours] = v
		}
	}
	if cwd := str(args, "Cwd"); filepath.IsAbs(cwd) {
		in.Cwd = cwd
	}
}

// antigravityOut is Antigravity's output contract. Peer mail goes in as a
// user message: its system messages are instructions, which mail never is.
func antigravityOut(kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop:
		return map[string]any{"decision": "continue", "reason": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"decision": "deny", "reason": res.Deny}
	case res.Warn != "" && kind == PreTool:
		return map[string]any{"decision": "allow", "reason": res.Warn}
	case res.Context != "" && (kind == Start || kind == Invoke):
		return map[string]any{"injectSteps": []any{map[string]any{"userMessage": res.Context}}}
	}
	return nil
}

// antigravityNothing is how Antigravity is told there's nothing to say: its
// decisions are required fields. "allow" leaves the user's own permissions
// to decide.
func antigravityNothing(kind Kind) map[string]any {
	switch kind {
	case PreTool:
		return map[string]any{"decision": "allow"}
	case Stop:
		return map[string]any{"decision": "stop"}
	}
	return map[string]any{}
}

// conversation is what a conversation's last model call was about.
type conversation struct {
	Input string `json:"input"` // hash of the user's latest input
	Held  bool   `json:"held"`  // first seen while peers held the agent's turn
}

// maxConversations are remembered per agent; the rest start over.
const maxConversations = 32

// userPrompted says whether a model call starts a prompt of the user's.
// Antigravity has no hook for that, only one before every call, so it is the
// user's when the latest input is new and isn't ainterlinked's. A
// conversation that appeared while peers held the agent's turn (a subagent
// it started) never counts: peers could keep it going forever.
func userPrompted(agent string, in Input) bool {
	if in.Prompt == "" || strings.Contains(in.Prompt, "[ainterlinked]") || in.Auto {
		return false
	}
	p := filepath.Join(config.Home(), "prompts", filepath.Base(agent)+".json")
	seen := map[string]conversation{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	if len(seen) > maxConversations {
		seen = map[string]conversation{}
	}
	sum := sha256.Sum256([]byte(in.Prompt))
	now := conversation{Input: hex.EncodeToString(sum[:8])}
	was, known := seen[in.SessionID]
	if now.Held = was.Held; !known {
		now.Held = Holds(agent) > 0
	}
	if was == now {
		return false
	}
	seen[in.SessionID] = now
	if b, err := json.Marshal(seen); err == nil {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if os.WriteFile(p, b, 0o600) != nil {
			return false // the next call would look like a prompt again
		}
	}
	return !now.Held
}
