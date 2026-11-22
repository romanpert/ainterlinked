// Command interlinked is a real-time bus for AI coding agents across
// harnesses (Claude Code, Codex, Copilot, Cursor, Gemini, OpenCode, ...).
package main

import (
	"os"

	"github.com/romanpert/ainterlinked/internal/cli"
)

func main() { os.Exit(cli.Main(os.Args[1:])) }
