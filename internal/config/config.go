// Package config holds ainterlinked's paths and user configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/romanpert/ainterlinked/internal/core"
	"github.com/romanpert/ainterlinked/internal/env"
)

// Config is the user configuration stored at <home>/config.toml.
type Config struct {
	// OldHub is a `hub = "<url>"` left from a version that could join
	// another machine's hub. The library serves one machine, so it is
	// ignored (doctor says so) and dropped on the next save.
	OldHub string `toml:"-"`
	// Machine is the name this machine shows to peers (defaults to hostname).
	Machine string `toml:"machine"`

	Delivery  Delivery              `toml:"delivery"`
	Presence  Presence              `toml:"presence"`
	Locks     Locks                 `toml:"locks"`
	Limits    core.Limits           `toml:"limits"`
	Covenant  Covenant              `toml:"covenant"`
	Server    Server                `toml:"server"`
	Harnesses map[string]HarnessCfg `toml:"harnesses"`
}

// Delivery controls how peer messages reach a model's context.
type Delivery struct {
	OnSessionStart bool `toml:"on_session_start"` // brief the agent on peers when a session starts
	OnPrompt       bool `toml:"on_prompt"`        // inject unread messages when the user submits a prompt
	OnTool         bool `toml:"on_tool"`          // inject unread messages after each tool call
	HoldStop       bool `toml:"hold_stop"`        // keep the agent working while it has unread messages
	ClaudeChannel  bool `toml:"claude_channel"`   // push into idle Claude Code sessions (needs --channels)
	MaxMessages    int  `toml:"max_messages"`     // per injection
	MaxChars       int  `toml:"max_chars"`        // per injection
	// InlineChars: a pushed message longer than this arrives as a gist with
	// its size and a pointer (get_thread); the reader pulls the rest only if
	// it wants it. Each agent can change it (set_delivery inline_chars).
	InlineChars int `toml:"inline_chars"`
	// WakeIdle starts a turn in an idle Claude Code or Codex session when mail
	// addressed to it by name arrives (Limits.MaxHolds times between two of
	// the user's prompts). Off: that turn is the user's money.
	WakeIdle bool `toml:"wake_idle"`
}

// Presence controls heartbeats and activity tracking.
type Presence struct {
	HeartbeatSeconds int  `toml:"heartbeat_seconds"`
	StaleSeconds     int  `toml:"stale_seconds"`
	TrackActivity    bool `toml:"track_activity"` // publish the last tool call (program or file, never arguments) as the agent's activity
	// SharePrompts publishes the start of each user prompt (credential-looking
	// text redacted) as the agent's activity. Off by default: prompts are the
	// user's words and redaction can't catch every secret.
	SharePrompts bool `toml:"share_prompts"`
}

// Locks controls file reservations.
type Locks struct {
	// Enforce is "deny" (block edits to files reserved by another live agent),
	// "warn" (allow, but tell the model) or "off".
	Enforce    string `toml:"enforce"`
	TTLMinutes int    `toml:"ttl_minutes"`
}

// Covenant controls how a repo's rules (.ainterlinked/covenant.md) reach agents.
type Covenant struct {
	// Show briefs the core rules at session start and holds back an agent's
	// first edit in an area that has rules, with those rules, once per session.
	Show bool `toml:"show"`
}

// Server configures `ainterlinked serve`.
type Server struct {
	Listen string `toml:"listen"`
}

// HarnessCfg is the per-harness switch. A disabled harness keeps its wiring
// but its hooks and MCP server stay silent.
type HarnessCfg struct {
	Enabled bool `toml:"enabled"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	host, _ := os.Hostname()
	return Config{
		Machine: strings.ToLower(host),
		Delivery: Delivery{
			OnSessionStart: true, OnPrompt: true, OnTool: true, HoldStop: true,
			ClaudeChannel: true, MaxMessages: 5, MaxChars: 4000, InlineChars: 600,
		},
		Presence:  Presence{HeartbeatSeconds: 10, StaleSeconds: 45, TrackActivity: true},
		Locks:     Locks{Enforce: "deny", TTLMinutes: 30},
		Limits:    core.DefaultLimits(),
		Covenant:  Covenant{Show: true},
		Server:    Server{Listen: "127.0.0.1:7717"},
		Harnesses: map[string]HarnessCfg{},
	}
}

// DirName is the data directory's name inside the user's home.
const DirName = ".ainterlinked"

// LegacyDirName is the data directory from before the rename to ainterlinked.
const LegacyDirName = ".interlinked"

// Home is the data directory: $AINTERLINKED_HOME, else ~/.ainterlinked. A
// machine that only has the old ~/.interlinked keeps using it (one hub, never
// two side by side) until it is renamed with every session closed; doctor
// says how.
func Home() string {
	if h := env.Get("HOME"); h != "" {
		return h
	}
	return homeIn(UserHome())
}

func homeIn(user string) string {
	cur := filepath.Join(user, DirName)
	if _, err := os.Stat(cur); err == nil {
		return cur
	}
	if fi, err := os.Stat(filepath.Join(user, LegacyDirName)); err == nil && fi.IsDir() {
		return filepath.Join(user, LegacyDirName)
	}
	return cur
}

// LegacyHome reports whether the data directory in use is the old
// ~/.interlinked (no AINTERLINKED_HOME and no ~/.ainterlinked yet).
func LegacyHome() bool {
	return env.Get("HOME") == "" && filepath.Base(Home()) == LegacyDirName
}

// UserHome is the user's home directory; AINTERLINKED_USER_HOME overrides it
// (used by tests and by installs into a sandbox).
func UserHome() string {
	if h := env.Get("USER_HOME"); h != "" {
		return h
	}
	h, err := env.OSHome()
	if err != nil {
		return "."
	}
	return h
}

// Path of the config file.
func Path() string { return filepath.Join(Home(), "config.toml") }

// DBPath of the local hub database.
func DBPath() string { return filepath.Join(Home(), "hub.db") }

// Load reads the config (for running).
func Load() (Config, error) { return LoadFile() }

// LoadFile reads the config file. Missing fields keep their defaults.
func LoadFile() (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(Path())
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	if _, err := toml.Decode(string(data), &cfg); err != nil {
		return cfg, fmt.Errorf("config %s: %w", Path(), err)
	}
	if err := cfg.Limits.Check(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", Path(), err)
	}
	var old struct {
		Hub string `toml:"hub"`
	}
	if _, err := toml.Decode(string(data), &old); err == nil {
		cfg.OldHub = old.Hub
	}
	if cfg.Harnesses == nil {
		cfg.Harnesses = map[string]HarnessCfg{}
	}
	return cfg, nil
}

// HarnessEnabled reports whether a harness may talk on the bus.
// Harnesses without an entry are enabled.
func (c Config) HarnessEnabled(name string) bool {
	if name == "copilot" {
		// The shared Copilot hooks serve both the CLI and VS Code: silent
		// only when both are muted.
		return c.HarnessEnabled("copilot-cli") || c.HarnessEnabled("vscode")
	}
	h, ok := c.Harnesses[name]
	return !ok || h.Enabled
}

// Save writes the config with a commented header.
func Save(cfg Config) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("# ainterlinked configuration — https://github.com/romanpert/ainterlinked\n")
	b.WriteString("# One machine, one hub (" + DBPath() + ").\n\n")
	if err := toml.NewEncoder(&b).Encode(cfg); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(Home(), ".config-*.toml")
	if err != nil {
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	_ = os.Chmod(tmp.Name(), 0o600)
	if err := os.Rename(tmp.Name(), Path()); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
