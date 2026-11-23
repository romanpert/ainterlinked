package hooks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	for ours, theirs := range map[string][]string{"file_path": {"TargetFile", "AbsolutePath", "Path"}, "command": {"CommandLine"}} {
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

// endedWell says whether a turn ended because the model was done. The docs
// call that model_stop; the CLI sends NO_TOOL_CALL.
func endedWell(in Input) bool {
	switch strings.ToLower(str(in.raw, "terminationReason")) {
	case "": // another harness
		return true
	case "model_stop", "no_tool_call":
		return str(in.raw, "error") == ""
	}
	return false
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

// conversationPath is a conversation's own file: the app runs the hooks of
// several at the same time, and none waits for another or writes over it.
func conversationPath(agent, id string) string {
	sum := sha256.Sum256([]byte(id))
	return filepath.Join(config.Home(), "prompts", filepath.Base(agent), hex.EncodeToString(sum[:8])+".json")
}

// readConversation says whether the conversation in p is known. One whose
// file is spoilt, or can't be read now (an error too), is taken for a
// subagent's, under way since the file was written.
func readConversation(p string) (c conversation, known bool, err error) {
	var b []byte
	for range 3 { // Windows refuses it while another hook replaces it
		if b, err = os.ReadFile(p); err == nil || errors.Is(err, fs.ErrNotExist) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return c, false, nil
	}
	if err == nil && json.Unmarshal(b, &c) == nil {
		return c, true, nil
	}
	seen := time.Now()
	if st, e := os.Stat(p); e == nil {
		seen = st.ModTime()
	}
	return conversation{Other: true, Open: true, Seen: seen.Unix()}, true, err
}

// conversations are all the agent's, by file.
func conversations(agent string) (map[string]conversation, error) {
	dir := filepath.Dir(conversationPath(agent, ""))
	files, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := map[string]conversation{}
	for _, f := range files {
		p := filepath.Join(dir, f.Name())
		if filepath.Ext(p) != ".json" {
			continue
		}
		if c, known, _ := readConversation(p); known {
			out[p] = c
		}
	}
	return out, nil
}

// saveConversation replaces the file whole: a hook that is killed halfway
// leaves the old one.
func saveConversation(p string, c conversation) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "*.tmp")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil && os.Rename(tmp.Name(), p) == nil {
		return nil
	}
	_ = os.Remove(tmp.Name())
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600) // Windows: another hook is reading it
}

// forgetOldest keeps the latest maxConversations of these.
func forgetOldest(seen map[string]conversation) {
	for len(seen) > maxConversations {
		oldest := ""
		for p, c := range seen {
			if oldest == "" || c.Seen < seen[oldest].Seen {
				oldest = p
			}
		}
		_ = os.Remove(oldest)
		delete(seen, oldest)
	}
}

// userPrompted says whether a model call starts a prompt of the user's.
// Antigravity has no hook for that, only one before every call, so it is the
// user's when the latest input is new and isn't ainterlinked's. (The briefing
// is the latest input once it is in, so the first is seen on SessionStart.)
// Nothing in it marks a subagent either: a conversation that appeared while
// another one's turn was under way, or while peers held the agent's turn, is
// taken for one and never counts, or peers could keep the agent going forever.
// That costs a conversation the user opens meanwhile its reset, the safe side.
func userPrompted(agent string, in Input) bool {
	now, p := time.Now(), conversationPath(agent, in.SessionID)
	c, known, err := readConversation(p)
	if err != nil {
		return false // it isn't lost: the next call may read it
	}
	if !known {
		seen, err := conversations(agent)
		c.Other = err != nil || Holds(agent) > 0 || underWay(seen, now)
		forgetOldest(seen)
	}
	fresh := false
	if hasInput(in) {
		sum := sha256.Sum256([]byte(in.Prompt))
		input := hex.EncodeToString(sum[:8])
		fresh = input != c.Input
		c.Input = input
	}
	c.Open, c.Seen = true, now.Unix()
	if saveConversation(p, c) != nil {
		return false // the next call would look like a prompt again
	}
	return fresh && !c.Other
}

// hasInput says whether the latest input isn't of our own making.
func hasInput(in Input) bool {
	return in.Prompt != "" && !in.Auto && !strings.Contains(in.Prompt, "[ainterlinked]")
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
	p := conversationPath(agent, in.SessionID)
	if c, known, err := readConversation(p); err == nil && known && c.Open {
		c.Open = false
		_ = saveConversation(p, c)
	}
}
