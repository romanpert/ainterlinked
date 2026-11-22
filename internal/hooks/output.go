package hooks

import (
	"encoding/json"
	"io"
)

// Write prints res in the dialect the harness expects. Nothing is printed
// when there's nothing to say (every harness treats empty output as "go on").
func Write(w io.Writer, harness, event string, kind Kind, res Result) {
	out := Format(harness, event, kind, res)
	if out == nil {
		return
	}
	b, _ := json.Marshal(out)
	w.Write(append(b, '\n'))
}

// Format builds the output object (nil = print nothing).
func Format(harness, event string, kind Kind, res Result) map[string]any {
	if res == (Result{}) {
		return nil
	}
	switch harness {
	case "cursor":
		return cursor(kind, res)
	case "copilot", "copilot-cli", "vscode":
		if isCamel(event) {
			return copilotCamel(kind, res)
		}
		return claudeLike(event, kind, res)
	case "gemini":
		return gemini(event, kind, res)
	case "opencode":
		return opencode(kind, res)
	case "grok":
		return grok(event, kind, res)
	}
	// Claude Code, Codex, Qwen Code, Muse Code, Devin and other Claude-compatible hooks.
	return claudeLike(event, kind, res)
}

func isCamel(event string) bool { return event != "" && event[0] >= 'a' && event[0] <= 'z' }

func claudeLike(event string, kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop:
		return map[string]any{"decision": "block", "reason": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": event, "permissionDecision": "deny", "permissionDecisionReason": res.Deny}}
	case res.Warn != "" && kind == PreTool:
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": res.Warn}}
	case res.Context != "" && (kind == Start || kind == Prompt || kind == PostTool):
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": res.Context}}
	}
	return nil
}

// grok takes Claude's output, but discards what SessionStart and
// UserPromptSubmit hooks print: mail waits there for an event that shows it.
// Only those two are dropped: a deny or a block always reaches Grok.
func grok(event string, kind Kind, res Result) map[string]any {
	if kind == Start || kind == Prompt {
		return nil
	}
	return claudeLike(event, kind, res)
}

func copilotCamel(kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop:
		return map[string]any{"decision": "block", "reason": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"permissionDecision": "deny", "permissionDecisionReason": res.Deny}
	case res.Context != "" && (kind == Start || kind == PostTool):
		// userPromptSubmitted has no documented context output.
		return map[string]any{"additionalContext": res.Context}
	}
	return nil
}

func gemini(event string, kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop: // AfterAgent: deny = retry with reason as the new prompt
		return map[string]any{"decision": "deny", "reason": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"decision": "deny", "reason": res.Deny}
	case res.Context != "" && (kind == Start || kind == Prompt || kind == PostTool):
		return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, "additionalContext": res.Context}}
	}
	return nil
}

func cursor(kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop:
		return map[string]any{"followup_message": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"permission": "deny", "user_message": res.Deny, "agent_message": res.Deny}
	case res.Context != "" && (kind == Start || kind == PostTool):
		return map[string]any{"additional_context": res.Context}
	}
	return nil
}

// opencode is read by the bundled plugin: context is appended to tool
// results, prompt wakes an idle session, deny throws in tool.execute.before.
func opencode(kind Kind, res Result) map[string]any {
	switch {
	case res.Block != "" && kind == Stop:
		return map[string]any{"prompt": res.Block}
	case res.Deny != "" && kind == PreTool:
		return map[string]any{"deny": res.Deny}
	case res.Context != "" && kind == PostTool:
		return map[string]any{"context": res.Context}
	}
	return nil
}
