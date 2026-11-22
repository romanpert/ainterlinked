package harness

import (
	"encoding/json"
	"strings"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

// antigravity covers the Antigravity app, CLI and IDE: they share
// ~/.gemini/config. Its hooks.json names each definition, so ours is one key.
func antigravity() *Harness {
	mcp := func(e *Env) string { return e.path(".gemini", "config", "mcp_config.json") }
	hooks := func(e *Env) string { return e.path(".gemini", "config", "hooks.json") }
	return &Harness{
		ID: "antigravity", Name: "Antigravity", Tier: "turn", Hooks: true,
		Notes: "Built from Antigravity's docs, not yet run against a real one. Conversations of one app process show up as one agent.",
		detect: func(e *Env) []string {
			return evidence(e, []string{"agy", "antigravity"}, e.path(".gemini", "config"), e.path(".gemini", "antigravity"),
				e.path(".gemini", "antigravity-cli"), e.path(".gemini", "antigravity-ide"))
		},
		install: func(e *Env, t *Tx) {
			t.EditJSON(mcp(e), func(old []byte) ([]byte, error) {
				return jsonc.Set(old, []string{"mcpServers", Server}, map[string]any{
					"command": e.Bin, "args": mcpArgs("antigravity"), "timeoutSeconds": 660})
			})
			t.EditJSON(hooks(e), func(old []byte) ([]byte, error) {
				def := antigravityHooks(e)
				// The user may have switched ours off without removing it.
				if v, ok := jsonc.Get(old, Server, "enabled"); ok && strings.TrimSpace(string(v)) == "false" {
					def["enabled"] = false
				}
				return jsonc.Set(old, []string{Server}, def)
			})
		},
		uninstall: func(e *Env, t *Tx) {
			for file, path := range map[string][]string{mcp(e): {"mcpServers", Server}, hooks(e): {Server}} {
				if !exists(file) {
					continue
				}
				t.EditJSON(file, func(old []byte) ([]byte, error) {
					out, _, err := jsonc.Delete(old, path)
					return out, err
				})
			}
		},
		wired: func(e *Env) Wiring {
			on, legacy := jsonWiring(mcp(e), "mcpServers")
			def, _ := jsonc.Get(read(hooks(e)), Server)
			return Wiring{MCP: on, Legacy: legacy, Hooks: antigravityOn(def), Files: []string{mcp(e), hooks(e)}}
		},
	}
}

func antigravityHooks(e *Env) map[string]any {
	run := func(event string) map[string]any {
		return map[string]any{"type": "command", "command": e.hookCmd("antigravity", event, false), "timeout": 15}
	}
	tools := func(matcher, event string) []any {
		return []any{map[string]any{"matcher": matcher, "hooks": []any{run(event)}}}
	}
	return map[string]any{
		"SessionStart":  []any{run("SessionStart")},
		"PreInvocation": []any{run("PreInvocation")},
		"PreToolUse":    tools("write_to_file|replace_file_content|multi_replace_file_content", "PreToolUse"),
		"PostToolUse":   tools("*", "PostToolUse"),
		"Stop":          []any{run("Stop")},
	}
}

// antigravityOn: our definition is there and not switched off.
func antigravityOn(def []byte) bool {
	var d struct {
		Enabled *bool           `json:"enabled"`
		Stop    json.RawMessage `json:"Stop"`
	}
	if json.Unmarshal(jsonc.Strip(def), &d) != nil {
		return false
	}
	return (d.Enabled == nil || *d.Enabled) && strings.Contains(string(d.Stop), " hook antigravity ")
}
