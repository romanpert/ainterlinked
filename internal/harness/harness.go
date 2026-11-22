// Package harness knows every supported AI coding harness: how to detect it
// on this machine and how to wire ainterlinked into its user-level config
// (MCP server + hooks), idempotently and reversibly.
package harness

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/romanpert/ainterlinked/internal/jsonc"
)

// Env is what an installer needs to know about the machine.
type Env struct {
	Home string // user home directory
	Bin  string // absolute path of the ainterlinked executable
	GOOS string
	// LookPath finds harness CLIs (overridable in tests).
	LookPath func(string) (string, error)
}

// NewEnv builds an Env for the current machine.
func NewEnv(home, bin string) *Env {
	return &Env{Home: home, Bin: bin, GOOS: runtime.GOOS, LookPath: exec.LookPath}
}

// Harness describes one supported tool.
type Harness struct {
	ID    string
	Name  string
	Tier  string // delivery once wired: push, turn or pull
	Hooks bool   // whether we wire lifecycle hooks
	Notes string // printed after install

	detect    func(e *Env) []string // evidence it's installed (paths/binaries)
	install   func(e *Env, t *Tx)
	uninstall func(e *Env, t *Tx)
	wired     func(e *Env) Wiring
}

// Server is the name the MCP server is registered under in every harness.
const Server = "ainterlinked"

// legacyServer is the name used before the rename. Install replaces an
// entry under it with Server; uninstall removes both.
const legacyServer = "interlinked"

// Wiring is what's currently configured.
type Wiring struct {
	MCP   bool
	Hooks bool
	// Legacy: an entry from before the rename ("interlinked") is still
	// there; `ainterlinked install` replaces it.
	Legacy bool
	Files  []string
}

// Detect returns evidence that the harness is installed (empty = not found).
func (h *Harness) Detect(e *Env) []string { return h.detect(e) }

// Install queues the changes that wire ainterlinked in (and take out what
// an older version wired under the name interlinked).
func (h *Harness) Install(e *Env, t *Tx) { h.install(e, t) }

// Uninstall queues the changes that remove ainterlinked, old names included.
func (h *Harness) Uninstall(e *Env, t *Tx) { h.uninstall(e, t) }

// Wired reports the current state.
func (h *Harness) Wired(e *Env) Wiring { return h.wired(e) }

// --- shared helpers ---

func (e *Env) path(parts ...string) string {
	return filepath.Join(append([]string{e.Home}, parts...)...)
}

// appData is the roaming app-data dir (Windows), Application Support (macOS)
// or XDG config dir (Linux), derived from Home so tests can sandbox it.
func (e *Env) appData(parts ...string) string {
	switch e.GOOS {
	case "windows":
		return e.path(append([]string{"AppData", "Roaming"}, parts...)...)
	case "darwin":
		return e.path(append([]string{"Library", "Application Support"}, parts...)...)
	}
	return e.path(append([]string{".config"}, parts...)...)
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func evidence(e *Env, bins []string, paths ...string) []string {
	var out []string
	for _, p := range paths {
		if exists(p) {
			out = append(out, p)
		}
	}
	for _, b := range bins {
		if lp, err := e.LookPath(b); err == nil {
			out = append(out, lp)
		}
	}
	return out
}

// binSlash is the binary path with forward slashes on Windows (they work in
// sh, bash, cmd, PowerShell and JS strings alike). It follows e.GOOS rather
// than the host, so a simulated Windows Env renders the same everywhere.
func (e *Env) binSlash() string {
	if e.GOOS == "windows" {
		return strings.ReplaceAll(e.Bin, `\`, "/")
	}
	return e.Bin
}

// cmdPath renders the binary for a shell command line; quoting is only added
// when needed.
func (e *Env) cmdPath(powershell bool) string {
	p := e.binSlash()
	if !strings.ContainsAny(p, " \t'\"&()") {
		return p
	}
	if powershell {
		return "& '" + strings.ReplaceAll(p, "'", "''") + "'"
	}
	return `"` + p + `"`
}

func (e *Env) hookCmd(harness, event string, powershell bool) string {
	return e.cmdPath(powershell) + " hook " + harness + " " + event
}

// ourCmd matches a command line we write: the ainterlinked executable, or
// the interlinked one from before the rename (any path, optionally quoted or
// PowerShell-invoked), followed by exactly "hook <harness> <event>". A
// user's own script that merely mentions the name doesn't match.
var ourCmd = regexp.MustCompile(`(?i)(^|[\s"'/\\&])a?interlinked(\.exe)?['"]?\s+hook\s+[\w.-]+\s+\w+\s*$`)

// ours recognises a single hook entry (one command object) we wrote.
func ours(raw []byte) bool {
	var h map[string]any
	if json.Unmarshal(jsonc.Strip(raw), &h) != nil {
		return false
	}
	for _, k := range []string{"command", "bash", "powershell"} {
		if s, ok := h[k].(string); ok && ourCmd.MatchString(strings.TrimSpace(s)) {
			return true
		}
	}
	return false
}

func read(p string) []byte {
	b, _ := os.ReadFile(p)
	return b
}

func jsonHas(p string, path ...string) bool {
	_, ok := jsonc.Get(read(p), path...)
	return ok
}

func mcpArgs(harness string) []string { return []string{"mcp", "--harness", harness} }

// hookSpec is one event to wire in a Claude-style hooks map
// ({"hooks": {"Event": [{"matcher": "...", "hooks": [{...}]}]}}).
type hookSpec struct {
	Event   string
	Matcher string
	Timeout int // seconds; 0 = the harness-wide default
}

// setClaudeStyleHooks replaces our entries under root.hooks (root = "" for
// a top-level "hooks" key) for each event.
func setClaudeStyleHooks(src []byte, e *Env, harness string, specs []hookSpec, timeout any, powershell bool) ([]byte, error) {
	src, err := removeClaudeStyleHooks(src, specs)
	if err != nil {
		return nil, err
	}
	for _, s := range specs {
		h := map[string]any{"type": "command", "command": e.hookCmd(harness, s.Event, powershell)}
		if timeout != nil {
			h["timeout"] = timeout
		}
		if s.Timeout > 0 {
			h["timeout"] = s.Timeout
		}
		group := map[string]any{"hooks": []any{h}}
		if s.Matcher != "" {
			group = map[string]any{"matcher": s.Matcher, "hooks": []any{h}}
		}
		if src, err = jsonc.Append(src, []string{"hooks", s.Event}, group); err != nil {
			return nil, err
		}
	}
	return src, nil
}

// removeClaudeStyleHooks removes only our own command entries from each
// matcher group; a group is deleted only when that leaves it empty, so a
// user's hook sharing a group with ours survives.
func removeClaudeStyleHooks(src []byte, specs []hookSpec) ([]byte, error) {
	seen := map[string]bool{}
	removed := 0
	for _, s := range specs {
		if seen[s.Event] {
			continue
		}
		seen[s.Event] = true
		ev := []string{"hooks", s.Event}
		for g := jsonc.Len(src, ev...) - 1; g >= 0; g-- {
			inner := append(append([]string{}, ev...), jsonc.Index(g), "hooks")
			out, n, err := jsonc.RemoveIf(src, inner, ours)
			if err != nil {
				return nil, err
			}
			src = out
			removed += n
			if n > 0 && jsonc.Len(src, inner...) == 0 {
				if src, err = jsonc.RemoveAt(src, ev, g); err != nil {
					return nil, err
				}
			}
		}
		if removed > 0 && jsonc.Len(src, ev...) == 0 {
			var err error
			if src, _, err = jsonc.Delete(src, ev); err != nil {
				return nil, err
			}
		}
	}
	if removed > 0 && jsonc.Len(src, "hooks") == 0 {
		src, _, _ = jsonc.Delete(src, []string{"hooks"})
	}
	return src, nil
}

// hasOurHooks reports whether any hook entry in a Claude-style or flat
// (Cursor) hooks map is ours.
func hasOurHooks(src []byte) bool {
	var m struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if json.Unmarshal(jsonc.Strip(src), &m) != nil {
		return false
	}
	for _, entries := range m.Hooks {
		for _, e := range entries {
			if ours(e) {
				return true
			}
			var g struct {
				Hooks []json.RawMessage `json:"hooks"`
			}
			if json.Unmarshal(e, &g) == nil {
				for _, h := range g.Hooks {
					if ours(h) {
						return true
					}
				}
			}
		}
	}
	return false
}

func addToList(src []byte, path []string, value string) ([]byte, error) {
	var list []string
	if _, err := jsonc.Unmarshal(src, &list, path...); err != nil {
		return nil, err
	}
	for _, v := range list {
		if v == value {
			return src, nil
		}
	}
	return jsonc.Append(src, path, value)
}

func removeFromList(src []byte, path []string, value string) ([]byte, error) {
	want, _ := json.Marshal(value)
	out, _, err := jsonc.RemoveIf(src, path, func(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), want) })
	return out, err
}

// under is key with name appended, as a fresh slice.
func under(key []string, name string) []string {
	return append(append([]string{}, key...), name)
}

// jsonServer wires {<key>: {"ainterlinked": entry}} into a JSON config file,
// dropping an entry an older version wrote under "interlinked".
func jsonServer(t *Tx, file string, key []string, entry any) {
	t.EditJSON(file, func(old []byte) ([]byte, error) {
		out, err := jsonc.Set(old, under(key, Server), entry)
		if err != nil {
			return nil, err
		}
		out, _, err = jsonc.Delete(out, under(key, legacyServer))
		return out, err
	})
}

// jsonServerRemove removes our entry, under either name.
func jsonServerRemove(t *Tx, file string, key []string) {
	if !jsonHas(file, under(key, Server)...) && !jsonHas(file, under(key, legacyServer)...) {
		return
	}
	t.EditJSON(file, func(old []byte) ([]byte, error) {
		out, _, err := jsonc.Delete(old, under(key, Server))
		if err != nil {
			return nil, err
		}
		out, _, err = jsonc.Delete(out, under(key, legacyServer))
		return out, err
	})
}

// jsonWiring reports whether file has our entry under key, and whether an
// old one is still there.
func jsonWiring(file string, key ...string) (mcp, legacy bool) {
	return jsonHas(file, under(key, Server)...), jsonHas(file, under(key, legacyServer)...)
}

// ToolNames lists the MCP tools (for harnesses that want an allow-list).
var ToolNames = []string{
	"whoami", "list_agents", "set_status", "set_delivery", "send_message", "read_inbox", "wait_for_messages", "get_thread",
	"create_task", "list_tasks", "list_boards", "claim_task", "want_task", "update_task",
	"reserve_files", "release_files", "list_reservations", "activity_feed", "handover",
}

//go:embed opencode_plugin.js
var opencodePlugin string
