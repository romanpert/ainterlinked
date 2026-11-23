package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/romanpert/ainterlinked/internal/config"
	"github.com/romanpert/ainterlinked/internal/hub"
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
	in.ToolDir = str(args, "Cwd")
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

// conversation is what is known of one of an agent's conversations.
type conversation struct {
	Input string `json:"input"` // hash of the latest input
	Other bool   `json:"other"` // it isn't one the user started, as far as can be told
	Open  bool   `json:"open"`  // its turn is under way
	Seen  int64  `json:"seen"`  // Unix seconds of its last model call
}

// maxConversations are remembered per agent, the latest ones.
const maxConversations = 32

// openFor is how long a turn counts as under way after its last model call
// when no Stop came (a crash, or perhaps a subagent's end, brings none). A
// subagent starts right after a model call of whoever starts it.
const openFor = 10 * time.Minute

func conversationsPath(agent string) string {
	return filepath.Join(config.Home(), "prompts", filepath.Base(agent)+".json")
}

func conversations(agent string) map[string]conversation {
	seen := map[string]conversation{}
	if b, err := os.ReadFile(conversationsPath(agent)); err == nil {
		_ = json.Unmarshal(b, &seen)
	}
	if seen == nil {
		seen = map[string]conversation{}
	}
	return seen
}

func saveConversations(agent string, seen map[string]conversation) error {
	for len(seen) > maxConversations {
		oldest := ""
		for id, c := range seen {
			if oldest == "" || c.Seen < seen[oldest].Seen {
				oldest = id
			}
		}
		delete(seen, oldest)
	}
	b, err := json.Marshal(seen)
	if err != nil {
		return err
	}
	p := conversationsPath(agent)
	// A new file in its place: a process that ends halfway leaves the old one.
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// lockWait is how long a hook waits for the others of its app.
const lockWait = 3 * time.Second

// lockConversations keeps the other hooks of this agent's app, which runs
// them at the same time, from writing over what this one reads and saves.
func lockConversations(agent string) (io.Closer, error) {
	p := conversationsPath(agent)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return hub.Lock(p+".lock", lockWait)
}

// userPrompted says whether a model call starts a prompt of the user's.
// Antigravity has no hook for that, only one before every call, so it is the
// user's when the latest input is new and isn't ainterlinked's. Nothing in it
// marks a subagent either: a conversation that appeared while another one's
// turn was under way, or while peers held the agent's turn, is taken for one
// and never counts, or peers could keep the agent going forever. That costs a
// conversation the user opens meanwhile its reset, the safe side.
func userPrompted(agent string, in Input) bool {
	l, err := lockConversations(agent)
	if err != nil {
		return false
	}
	defer l.Close()
	seen, now := conversations(agent), time.Now()
	c, known := seen[in.SessionID]
	if !known {
		c.Other = Holds(agent) > 0 || underWay(seen, now)
	}
	fresh := false
	if in.Prompt != "" && !in.Auto && !strings.Contains(in.Prompt, "[ainterlinked]") {
		sum := sha256.Sum256([]byte(in.Prompt))
		input := hex.EncodeToString(sum[:8])
		fresh = input != c.Input
		c.Input = input
	}
	c.Open, c.Seen = true, now.Unix()
	seen[in.SessionID] = c
	if saveConversations(agent, seen) != nil {
		return false // the next call would look like a prompt again
	}
	return fresh && !c.Other
}

// underWay says whether any of these conversations is in the middle of a turn.
func underWay(seen map[string]conversation, now time.Time) bool {
	for _, c := range seen {
		if c.Open && now.Sub(time.Unix(c.Seen, 0)) < openFor {
			return true
		}
	}
	return false
}

// turnOver notes that a conversation's turn ended. With work of its own still
// running in the background it hasn't: a subagent may yet appear.
func turnOver(agent string, in Input) {
	if idle, ok := in.raw["fullyIdle"].(bool); ok && !idle {
		return
	}
	l, err := lockConversations(agent)
	if err != nil {
		return
	}
	defer l.Close()
	seen := conversations(agent)
	c, known := seen[in.SessionID]
	if !known || !c.Open {
		return
	}
	c.Open = false
	seen[in.SessionID] = c
	_ = saveConversations(agent, seen)
}
