// Package config holds interlinked's paths and user configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is the user configuration stored at <home>/config.toml.
type Config struct {
	// Hub is empty for the local SQLite hub, or the URL of a remote
	// `interlinked serve` (e.g. http://100.64.0.2:7717) to join another machine.
	Hub   string `toml:"hub"`
	Token string `toml:"token"`
	// Machine is the name this machine shows to peers (defaults to hostname).
	Machine string `toml:"machine"`

	Delivery  Delivery              `toml:"delivery"`
	Presence  Presence              `toml:"presence"`
	Locks     Locks                 `toml:"locks"`
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

// Server configures `interlinked serve`.
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
			ClaudeChannel: true, MaxMessages: 5, MaxChars: 4000,
		},
		Presence:  Presence{HeartbeatSeconds: 10, StaleSeconds: 45, TrackActivity: true},
		Locks:     Locks{Enforce: "deny", TTLMinutes: 30},
		Server:    Server{Listen: "127.0.0.1:7717"},
		Harnesses: map[string]HarnessCfg{},
	}
}

// Home is the interlinked data directory ($INTERLINKED_HOME or ~/.interlinked).
func Home() string {
	if h := os.Getenv("INTERLINKED_HOME"); h != "" {
		return h
	}
	return filepath.Join(UserHome(), ".interlinked")
}

// UserHome is the user's home directory; INTERLINKED_USER_HOME overrides it
// (used by tests and by installs into a sandbox).
func UserHome() string {
	if h := os.Getenv("INTERLINKED_USER_HOME"); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// Path of the config file.
func Path() string { return filepath.Join(Home(), "config.toml") }

// DBPath of the local hub database.
func DBPath() string { return filepath.Join(Home(), "hub.db") }

// Load reads the config with environment overrides applied (for running).
func Load() (Config, error) {
	cfg, err := LoadFile()
	return applyEnv(cfg), err
}

// LoadFile reads the config file only, without environment overrides (for
// editing and saving it back). Missing fields keep their defaults.
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
	if cfg.Harnesses == nil {
		cfg.Harnesses = map[string]HarnessCfg{}
	}
	return cfg, nil
}

func applyEnv(cfg Config) Config {
	if v := os.Getenv("INTERLINKED_HUB"); v != "" {
		cfg.Hub = v
	}
	if v := os.Getenv("INTERLINKED_TOKEN"); v != "" {
		cfg.Token = v
	}
	return cfg
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
	b.WriteString("# interlinked configuration — https://github.com/romanpert/interlinked\n")
	b.WriteString("# hub = \"\" uses the local hub; set it to http://<host>:7717 to join another machine.\n\n")
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
