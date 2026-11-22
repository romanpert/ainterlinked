package harness

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

// ---------------------------------------------------------------- Muse Code

// museHooks: Muse Code's hooks take Claude's output schema. Its file tools
// answer to the names Edit and Write in matchers.
var museHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: "Edit|Write"},
	{Event: "PostToolUse"},
	{Event: "Stop"},
	{Event: "SessionEnd"},
}

func muse() *Harness {
	settings := func(e *Env) string { return e.path(".config", "muse", "settings.json") }
	// Muse Code reads the server under mcpServers, or under mcp_servers in
	// files written before 1.3.
	keys := []string{"mcpServers", "mcp_servers"}
	return &Harness{
		ID: "muse", Name: "Muse Code", Tier: "turn", Hooks: true,
		Notes:  "Built from Muse Code's 1.4 docs, not yet run against a real muse. If XDG_CONFIG_HOME is set, muse reads its settings from there instead.",
		detect: func(e *Env) []string { return evidence(e, []string{"muse"}, e.path(".config", "muse")) },
		install: func(e *Env, t *Tx) {
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				if len(strings.TrimSpace(string(old))) == 0 {
					// Without schema_version every muse command fails to start.
					old = []byte("{\n  \"schema_version\": 1\n}\n")
				}
				out, err := removeServers(old, keys)
				if err != nil {
					return nil, err
				}
				// Mixing both spellings in one file disables MCP for the
				// session, so the entry goes where the user's servers are.
				key := keys[0]
				if _, legacy := jsonc.Get(out, keys[1]); legacy {
					key = keys[1]
				}
				entry := map[string]any{"command": e.Bin, "args": mcpArgs("muse")}
				if key == keys[1] {
					entry["transport"], entry["mode"] = "stdio", "optional"
				}
				if out, err = jsonc.Set(out, []string{key, Server}, entry); err != nil {
					return nil, err
				}
				return setClaudeStyleHooks(out, e, "muse", museHooks, 15, false)
			})
		},
		uninstall: func(e *Env, t *Tx) {
			if !exists(settings(e)) {
				return
			}
			t.EditJSON(settings(e), func(old []byte) ([]byte, error) {
				out, err := removeServers(old, keys)
				if err != nil {
					return nil, err
				}
				return removeClaudeStyleHooks(out, museHooks)
			})
		},
		wired: func(e *Env) Wiring {
			w := Wiring{Hooks: hasOurHooks(read(settings(e))), Files: []string{settings(e)}}
			for _, k := range keys {
				on, legacy := jsonWiring(settings(e), k)
				w.MCP, w.Legacy = w.MCP || on, w.Legacy || legacy
			}
			return w
		},
	}
}

// removeServers drops our server, under either of its names, from each key.
func removeServers(src []byte, keys []string) ([]byte, error) {
	for _, k := range keys {
		for _, name := range []string{Server, legacyServer} {
			if _, ok := jsonc.Get(src, k, name); !ok {
				continue
			}
			var err error
			if src, _, err = jsonc.Delete(src, []string{k, name}); err != nil {
				return nil, err
			}
		}
	}
	return src, nil
}

// ---------------------------------------------------------------- Grok Build

// grokHooks: Grok Build's hooks take Claude's event names and output. Its
// default timeout is 5 s, so ours is always explicit.
var grokHooks = []hookSpec{
	{Event: "SessionStart"},
	{Event: "UserPromptSubmit"},
	{Event: "PreToolUse", Matcher: "Edit|Write|MultiEdit|NotebookEdit"},
	{Event: "PostToolUse"},
	{Event: "Stop"},
	{Event: "SessionEnd"},
}

func grok() *Harness {
	cfg := func(e *Env) string { return e.path(".grok", "config.toml") }
	hooks := func(e *Env) string { return e.path(".grok", "hooks", Server+".json") } // ours alone
	block := func(e *Env) string {
		return fmt.Sprintf("command = %s\nargs = [\"mcp\", \"--harness\", \"grok\"]\nstartup_timeout_sec = 20\ntool_timeout_sec = 660\n", tomlString(e.Bin))
	}
	return &Harness{
		ID: "grok", Name: "Grok Build", Tier: "turn", Hooks: true,
		Notes:  "Built from Grok Build's published source, not yet run against a real grok. Grok drops hook context at session start and on prompts, so mail arrives after tool calls and when a turn ends.",
		detect: func(e *Env) []string { return evidence(e, []string{"grok"}, e.path(".grok")) },
		install: func(e *Env, t *Tx) {
			t.EditTOML(cfg(e), func(old []byte) ([]byte, error) {
				src, _ := tomlRemoveTable(string(old), "mcp_servers."+legacyServer)
				return []byte(tomlSetTable(src, "mcp_servers."+Server, block(e))), nil
			})
			file, err := setClaudeStyleHooks([]byte("{}\n"), e, "grok", grokHooks, 15, false)
			if err != nil { // can't happen on "{}"
				panic(err)
			}
			t.Write(hooks(e), file)
		},
		uninstall: func(e *Env, t *Tx) {
			if exists(cfg(e)) {
				t.EditTOML(cfg(e), func(old []byte) ([]byte, error) {
					out, _ := tomlRemoveTable(string(old), "mcp_servers."+Server)
					out, _ = tomlRemoveTable(out, "mcp_servers."+legacyServer)
					return []byte(out), nil
				})
			}
			t.Remove(hooks(e))
		},
		wired: func(e *Env) Wiring {
			src := string(read(cfg(e)))
			return Wiring{MCP: tomlHasTable(src, "mcp_servers."+Server), Legacy: tomlHasTable(src, "mcp_servers."+legacyServer),
				Hooks: hasOurHooks(read(hooks(e))), Files: []string{cfg(e), filepath.Clean(hooks(e))}}
		},
	}
}
