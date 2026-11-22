package harness

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

// All returns every supported harness, in display order.
func All() []*Harness {
	return []*Harness{
		claudeCode(), codex(), copilotCLI(), vscode(), cursor(), gemini(), opencode(),
		qwen(), windsurf(), kiro(), zed(), cline(), crush(),
	}
}

// ByID finds a harness by id or common alias.
func ByID(id string) *Harness {
	aliases := map[string]string{"claude": "claude-code", "copilot": "copilot-cli", "code": "vscode", "vs-code": "vscode", "devin": "windsurf"}
	if a, ok := aliases[strings.ToLower(id)]; ok {
		id = a
	}
	for _, h := range All() {
		if h.ID == strings.ToLower(id) {
			return h
		}
	}
	return nil
}

// ---------------------------------------------------------------- Claude Code

var claudeHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit|NotebookEdit"},
	{Event: "PostToolUse"},
	{Event: "Stop"},
	{Event: "SessionEnd"},
}

func claudeCode() *Harness {
	settings := func(e *Env) string { return e.path(".claude", "settings.json") }
	userJSON := func(e *Env) string { return e.path(".claude.json") }
	entry := func(e *Env) map[string]any {
		return map[string]any{"type": "stdio", "command": e.Bin, "args": mcpArgs("claude-code"), "env": map[string]any{}}
	}
	return &Harness{
		ID: "claude-code", Name: "Claude Code", Tier: "turn", Hooks: true,
		Notes: "Push into idle sessions: start Claude with `interlinked launch claude` (enables the claude/channel push).",
		detect: func(e *Env) []string {
			return evidence(e, []string{"claude"}, e.path(".claude"), userJSON(e))
		},
		install: func(e *Env, t *Tx) {
			// ~/.claude.json is rewritten by running sessions, so prefer the
			// official CLI (it owns that file) and fall back to a careful edit.
			if claude, err := e.LookPath("claude"); err == nil {
				raw, _ := jsonMarshal(entry(e))
				t.RunOptional("claude mcp remove (refresh)", claude, "mcp", "remove", "--scope", "user", "interlinked")
				t.Run("claude mcp add-json --scope user interlinked", claude, "mcp", "add-json", "--scope", "user", "interlinked", raw)
			} else {
				jsonServer(t, userJSON(e), []string{"mcpServers"}, entry(e))
			}
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, err := setClaudeStyleHooks(old, e, "claude-code", claudeHooks, 15, false)
				if err != nil {
					return nil, err
				}
				// Let the model use the bus without a permission prompt per call.
				return addToList(out, []string{"permissions", "allow"}, "mcp__interlinked")
			})
		},
		uninstall: func(e *Env, t *Tx) {
			if claude, err := e.LookPath("claude"); err == nil && jsonHas(userJSON(e), "mcpServers", "interlinked") {
				t.Run("claude mcp remove --scope user interlinked", claude, "mcp", "remove", "--scope", "user", "interlinked")
			} else {
				jsonServerRemove(t, userJSON(e), []string{"mcpServers"})
			}
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, err := removeClaudeStyleHooks(old, claudeHooks)
				if err != nil {
					return nil, err
				}
				return removeFromList(out, []string{"permissions", "allow"}, "mcp__interlinked")
			})
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(userJSON(e), "mcpServers", "interlinked"), Hooks: hasOurHooks(read(settings(e))),
				Files: []string{userJSON(e), settings(e)}}
		},
	}
}

// ---------------------------------------------------------------- Codex

var codexHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: "apply_patch|Edit|Write"},
	{Event: "PostToolUse"},
	{Event: "Stop"},
	{Event: "SessionEnd", Timeout: 3}, // Codex clamps SessionEnd to 3 s
}

func codexHome(e *Env) string { return e.path(".codex") }

func codex() *Harness {
	cfg := func(e *Env) string { return filepath.Join(codexHome(e), "config.toml") }
	hooks := func(e *Env) string { return filepath.Join(codexHome(e), "hooks.json") }
	block := func(e *Env) string {
		// default_tools_approval_mode: without it Codex asks before every
		// bus call (and `codex exec` refuses them). The tools only message,
		// plan and reserve files, the same trust mcp__interlinked gets in Claude.
		return fmt.Sprintf("command = %s\nargs = [\"mcp\", \"--harness\", \"codex\"]\nstartup_timeout_sec = 20\ntool_timeout_sec = 660\ndefault_tools_approval_mode = \"approve\"\n", tomlString(e.Bin))
	}
	return &Harness{
		ID: "codex", Name: "OpenAI Codex", Tier: "turn", Hooks: true,
		Notes:  "Codex skips new hooks until you trust them: open Codex and run /hooks to approve the interlinked hooks.",
		detect: func(e *Env) []string { return evidence(e, []string{"codex"}, codexHome(e)) },
		install: func(e *Env, t *Tx) {
			t.EditTOML(cfg(e), func(old []byte) ([]byte, error) {
				return []byte(tomlSetTable(string(old), "mcp_servers.interlinked", block(e))), nil
			})
			t.EditJSON(hooks(e), func(old []byte) ([]byte, error) {
				return setClaudeStyleHooks(old, e, "codex", codexHooks, 15, false)
			})
		},
		uninstall: func(e *Env, t *Tx) {
			t.EditTOML(cfg(e), func(old []byte) ([]byte, error) {
				out, _ := tomlRemoveTable(string(old), "mcp_servers.interlinked")
				return []byte(out), nil
			})
			if exists(hooks(e)) {
				t.EditJSON(hooks(e), func(old []byte) ([]byte, error) { return removeClaudeStyleHooks(old, codexHooks) })
			}
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: tomlHasTable(string(read(cfg(e))), "mcp_servers.interlinked"), Hooks: hasOurHooks(read(hooks(e))),
				Files: []string{cfg(e), hooks(e)}}
		},
	}
}

// ---------------------------------------------------------------- Copilot

// copilotHookEvents are wired in ~/.copilot/hooks/interlinked.json, which
// both the Copilot CLI and VS Code agent hooks read.
var copilotHookEvents = []string{"sessionStart", "userPromptSubmitted", "preToolUse", "postToolUse", "agentStop", "sessionEnd"}

func copilotHooksFile(e *Env) string { return e.path(".copilot", "hooks", "interlinked.json") }

func copilotHooks(e *Env) []byte {
	hooks := map[string]any{}
	for _, ev := range copilotHookEvents {
		hooks[ev] = []any{map[string]any{
			"type":       "command",
			"bash":       e.hookCmd("copilot", ev, false),
			"powershell": e.hookCmd("copilot", ev, true),
			"timeoutSec": 15,
		}}
	}
	b, _ := jsonIndent(map[string]any{"version": 1, "hooks": hooks})
	return b
}

func copilotCLI() *Harness {
	mcp := func(e *Env) string { return e.path(".copilot", "mcp-config.json") }
	return &Harness{
		ID: "copilot-cli", Name: "GitHub Copilot CLI", Tier: "turn", Hooks: true,
		detect: func(e *Env) []string { return evidence(e, []string{"copilot"}, e.path(".copilot")) },
		install: func(e *Env, t *Tx) {
			jsonServer(t, mcp(e), []string{"mcpServers"}, map[string]any{
				"type": "local", "command": e.Bin, "args": mcpArgs("copilot-cli"), "tools": []string{"*"}})
			t.Write(copilotHooksFile(e), copilotHooks(e))
		},
		uninstall: func(e *Env, t *Tx) {
			jsonServerRemove(t, mcp(e), []string{"mcpServers"})
			if !ByID("vscode").Wired(e).MCP { // VS Code agent hooks share this file
				t.Remove(copilotHooksFile(e))
			}
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(mcp(e), "mcpServers", "interlinked"), Hooks: exists(copilotHooksFile(e)),
				Files: []string{mcp(e), copilotHooksFile(e)}}
		},
	}
}

func vscode() *Harness {
	dirs := func(e *Env) []string {
		return []string{e.appData("Code", "User"), e.appData("Code - Insiders", "User")}
	}
	return &Harness{
		ID: "vscode", Name: "VS Code (Copilot agent)", Tier: "turn", Hooks: true,
		Notes: "VS Code agent hooks are a preview feature; without them VS Code still gets the MCP tools (pull).",
		detect: func(e *Env) []string {
			var ev []string
			for _, d := range dirs(e) {
				if exists(d) {
					ev = append(ev, d)
				}
			}
			return append(ev, evidence(e, []string{"code"})...)
		},
		install: func(e *Env, t *Tx) {
			for _, d := range dirs(e) {
				if exists(d) {
					jsonServer(t, filepath.Join(d, "mcp.json"), []string{"servers"}, map[string]any{
						"type": "stdio", "command": e.Bin, "args": mcpArgs("vscode")})
				}
			}
			t.Write(copilotHooksFile(e), copilotHooks(e))
		},
		uninstall: func(e *Env, t *Tx) {
			for _, d := range dirs(e) {
				jsonServerRemove(t, filepath.Join(d, "mcp.json"), []string{"servers"})
			}
			if !ByID("copilot-cli").Wired(e).MCP {
				t.Remove(copilotHooksFile(e))
			}
		},
		wired: func(e *Env) Wiring {
			w := Wiring{Hooks: exists(copilotHooksFile(e))}
			for _, d := range dirs(e) {
				f := filepath.Join(d, "mcp.json")
				w.Files = append(w.Files, f)
				w.MCP = w.MCP || jsonHas(f, "servers", "interlinked")
			}
			return w
		},
	}
}

// ---------------------------------------------------------------- Cursor

var cursorHookEvents = []string{"sessionStart", "beforeSubmitPrompt", "preToolUse", "postToolUse", "stop", "sessionEnd"}

func cursor() *Harness {
	mcp := func(e *Env) string { return e.path(".cursor", "mcp.json") }
	hooks := func(e *Env) string { return e.path(".cursor", "hooks.json") }
	ps := func(e *Env) bool { return e.GOOS == "windows" } // Cursor runs hooks through PowerShell on Windows
	return &Harness{
		ID: "cursor", Name: "Cursor", Tier: "turn", Hooks: true,
		detect: func(e *Env) []string { return evidence(e, []string{"cursor"}, e.path(".cursor")) },
		install: func(e *Env, t *Tx) {
			jsonServer(t, mcp(e), []string{"mcpServers"}, map[string]any{"command": e.Bin, "args": mcpArgs("cursor")})
			t.EditJSON(hooks(e), func(old []byte) ([]byte, error) {
				out := old
				if len(strings.TrimSpace(string(out))) == 0 {
					out = []byte("{\n  \"version\": 1\n}\n")
				}
				var err error
				for _, ev := range cursorHookEvents {
					if out, _, err = jsonc.RemoveIf(out, []string{"hooks", ev}, ours); err != nil {
						return nil, err
					}
					entry := map[string]any{"command": e.hookCmd("cursor", ev, ps(e)), "timeout": 15}
					if ev == "stop" {
						entry["loop_limit"] = 5
					}
					if out, err = jsonc.Append(out, []string{"hooks", ev}, entry); err != nil {
						return nil, err
					}
				}
				return out, nil
			})
		},
		uninstall: func(e *Env, t *Tx) {
			jsonServerRemove(t, mcp(e), []string{"mcpServers"})
			if !exists(hooks(e)) {
				return
			}
			t.EditJSON(hooks(e), func(old []byte) ([]byte, error) {
				out := old
				for _, ev := range cursorHookEvents {
					var n int
					var err error
					if out, n, err = jsonc.RemoveIf(out, []string{"hooks", ev}, ours); err != nil {
						return nil, err
					}
					if n > 0 && jsonc.Len(out, "hooks", ev) == 0 {
						out, _, _ = jsonc.Delete(out, []string{"hooks", ev})
					}
				}
				return out, nil
			})
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(mcp(e), "mcpServers", "interlinked"), Hooks: hasOurHooks(read(hooks(e))), Files: []string{mcp(e), hooks(e)}}
		},
	}
}

// ---------------------------------------------------------------- Gemini CLI

var geminiHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "BeforeAgent"},
	{Event: "BeforeTool", Matcher: "write_file|replace|edit"},
	{Event: "AfterTool"},
	{Event: "AfterAgent"},
	{Event: "SessionEnd"},
}

func gemini() *Harness {
	settings := func(e *Env) string { return e.path(".gemini", "settings.json") }
	return &Harness{
		ID: "gemini", Name: "Gemini CLI", Tier: "turn", Hooks: true,
		detect: func(e *Env) []string { return evidence(e, []string{"gemini"}, e.path(".gemini")) },
		install: func(e *Env, t *Tx) {
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, err := jsonc.Set(old, []string{"mcpServers", "interlinked"}, map[string]any{
					"command": e.Bin, "args": mcpArgs("gemini"), "trust": true, "timeout": 660000})
				if err != nil {
					return nil, err
				}
				return setClaudeStyleHooks(out, e, "gemini", geminiHooks, 15000, false) // ms
			})
		},
		uninstall: func(e *Env, t *Tx) {
			if !exists(settings(e)) {
				return
			}
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, _, err := jsonc.Delete(old, []string{"mcpServers", "interlinked"})
				if err != nil {
					return nil, err
				}
				return removeClaudeStyleHooks(out, geminiHooks)
			})
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(settings(e), "mcpServers", "interlinked"), Hooks: hasOurHooks(read(settings(e))), Files: []string{settings(e)}}
		},
	}
}

// ---------------------------------------------------------------- Qwen Code

var qwenHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: "write_file|replace|edit"},
	{Event: "PostToolUse"},
	{Event: "Stop"},
	{Event: "SessionEnd"},
}

func qwen() *Harness {
	settings := func(e *Env) string { return e.path(".qwen", "settings.json") }
	return &Harness{
		ID: "qwen", Name: "Qwen Code", Tier: "turn", Hooks: true,
		detect: func(e *Env) []string { return evidence(e, []string{"qwen"}, e.path(".qwen")) },
		install: func(e *Env, t *Tx) {
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, err := jsonc.Set(old, []string{"mcpServers", "interlinked"}, map[string]any{
					"command": e.Bin, "args": mcpArgs("qwen"), "trust": true})
				if err != nil {
					return nil, err
				}
				return setClaudeStyleHooks(out, e, "qwen", qwenHooks, nil, false)
			})
		},
		uninstall: func(e *Env, t *Tx) {
			if !exists(settings(e)) {
				return
			}
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, _, err := jsonc.Delete(old, []string{"mcpServers", "interlinked"})
				if err != nil {
					return nil, err
				}
				return removeClaudeStyleHooks(out, qwenHooks)
			})
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(settings(e), "mcpServers", "interlinked"), Hooks: hasOurHooks(read(settings(e))), Files: []string{settings(e)}}
		},
	}
}

// ---------------------------------------------------------------- OpenCode

func opencode() *Harness {
	dir := func(e *Env) string { return e.path(".config", "opencode") }
	cfg := func(e *Env) string {
		if p := filepath.Join(dir(e), "opencode.jsonc"); exists(p) {
			return p
		}
		return filepath.Join(dir(e), "opencode.json")
	}
	plugin := func(e *Env) string { return filepath.Join(dir(e), "plugins", "interlinked.js") }
	return &Harness{
		ID: "opencode", Name: "OpenCode", Tier: "push", Hooks: true,
		Notes:  "The OpenCode plugin wakes idle sessions when a message arrives (push).",
		detect: func(e *Env) []string { return evidence(e, []string{"opencode"}, dir(e)) },
		install: func(e *Env, t *Tx) {
			t.EditJSON(cfg(e), func(old []byte) ([]byte, error) {
				if len(strings.TrimSpace(string(old))) == 0 {
					old = []byte("{\n  \"$schema\": \"https://opencode.ai/config.json\"\n}\n")
				}
				return jsonc.Set(old, []string{"mcp", "interlinked"}, map[string]any{
					"type": "local", "command": append([]string{e.Bin}, mcpArgs("opencode")...), "enabled": true})
			})
			t.Write(plugin(e), []byte(strings.ReplaceAll(opencodePlugin, "__INTERLINKED_BIN__", e.binSlash())))
		},
		uninstall: func(e *Env, t *Tx) {
			jsonServerRemove(t, cfg(e), []string{"mcp"})
			t.Remove(plugin(e))
		},
		wired: func(e *Env) Wiring {
			return Wiring{MCP: jsonHas(cfg(e), "mcp", "interlinked"), Hooks: exists(plugin(e)), Files: []string{cfg(e), plugin(e)}}
		},
	}
}

// ---------------------------------------------------------------- MCP-only harnesses

func mcpOnly(id, name string, file func(e *Env) []string, key []string, entry func(e *Env) map[string]any, bins []string, notes string) *Harness {
	return &Harness{
		ID: id, Name: name, Tier: "pull", Notes: notes,
		detect: func(e *Env) []string {
			var paths []string
			for _, f := range file(e) {
				paths = append(paths, f, filepath.Dir(f))
			}
			return dedupe(evidence(e, bins, paths...))
		},
		install: func(e *Env, t *Tx) {
			for _, f := range file(e) {
				if exists(f) || exists(filepath.Dir(f)) {
					jsonServer(t, f, key, entry(e))
				}
			}
		},
		uninstall: func(e *Env, t *Tx) {
			for _, f := range file(e) {
				jsonServerRemove(t, f, key)
			}
		},
		wired: func(e *Env) Wiring {
			w := Wiring{}
			for _, f := range file(e) {
				w.Files = append(w.Files, f)
				w.MCP = w.MCP || jsonHas(f, append(append([]string{}, key...), "interlinked")...)
			}
			return w
		},
	}
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

const pullNote = "No hooks API: the agent sees messages when it calls read_inbox / wait_for_messages."

func windsurf() *Harness {
	return mcpOnly("windsurf", "Windsurf", func(e *Env) []string { return []string{e.path(".codeium", "windsurf", "mcp_config.json")} },
		[]string{"mcpServers"}, func(e *Env) map[string]any { return map[string]any{"command": e.Bin, "args": mcpArgs("windsurf")} },
		[]string{"windsurf"}, pullNote)
}

func kiro() *Harness {
	return mcpOnly("kiro", "Kiro", func(e *Env) []string { return []string{e.path(".kiro", "settings", "mcp.json")} },
		[]string{"mcpServers"}, func(e *Env) map[string]any {
			return map[string]any{"command": e.Bin, "args": mcpArgs("kiro"), "disabled": false, "autoApprove": ToolNames}
		}, []string{"kiro"}, pullNote)
}

func zed() *Harness {
	return mcpOnly("zed", "Zed", func(e *Env) []string {
		if e.GOOS == "windows" {
			return []string{e.appData("Zed", "settings.json")}
		}
		return []string{e.path(".config", "zed", "settings.json")}
	}, []string{"context_servers"}, func(e *Env) map[string]any {
		return map[string]any{"command": e.Bin, "args": mcpArgs("zed"), "env": map[string]any{}}
	}, []string{"zed"}, pullNote)
}

func cline() *Harness {
	return mcpOnly("cline", "Cline", func(e *Env) []string {
		return []string{
			e.appData("Code", "User", "globalStorage", "saoudrizwan.claude-dev", "settings", "cline_mcp_settings.json"),
			e.path(".cline", "data", "settings", "cline_mcp_settings.json"),
		}
	}, []string{"mcpServers"}, func(e *Env) map[string]any {
		return map[string]any{"command": e.Bin, "args": mcpArgs("cline"), "disabled": false, "autoApprove": ToolNames}
	}, []string{"cline"}, pullNote)
}

func crush() *Harness {
	h := mcpOnly("crush", "Crush", func(e *Env) []string { return []string{e.path(".config", "crush", "crush.json")} },
		[]string{"mcp"}, func(e *Env) map[string]any {
			return map[string]any{"type": "stdio", "command": e.Bin, "args": mcpArgs("crush")}
		}, []string{"crush"}, pullNote)
	return h
}
