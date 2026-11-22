# Harness reference

This page lists what `ainterlinked install` writes for each harness, where it writes it, and how well each integration is verified. Everything goes into user-level config, and every edited file gets a `*.bak-ainterlinked-<timestamp>` copy first. Paths use `~` for your home directory, which is `%USERPROFILE%` on Windows.

The **Verified** column says how each integration was checked:

- **live**: exercised against the real harness.
- **e2e**: tested with real processes emulating the harness.
- **docs**: built from the vendor's current documentation and covered by unit tests, but not yet run against the real harness.

| Harness | MCP config | Hooks | Delivery | Verified |
|---|---|---|---|---|
| Claude Code | `claude mcp add-json --scope user` (writes `~/.claude.json`) | `~/.claude/settings.json` | turn; push with `ainterlinked launch claude` | live (Claude Code 2.1.287: SessionStart briefing, PostToolUse injection, Stop-hold, MCP tools) |
| OpenAI Codex | `~/.codex/config.toml` `[mcp_servers.ainterlinked]` | `~/.codex/hooks.json` | turn | docs + e2e (same output contract as Claude Code) |
| GitHub Copilot CLI | `~/.copilot/mcp-config.json` | `~/.copilot/hooks/ainterlinked.json` | turn | docs |
| VS Code (Copilot agent) | `<VS Code user dir>/mcp.json` `servers` | shares `~/.copilot/hooks/` (VS Code agent hooks are a preview feature) | turn, or pull without hooks | docs |
| Cursor | `~/.cursor/mcp.json` | `~/.cursor/hooks.json` | turn | docs |
| Gemini CLI | `~/.gemini/settings.json` `mcpServers` | same file, `hooks` | turn | docs |
| OpenCode | `~/.config/opencode/opencode.json` `mcp` | plugin `~/.config/opencode/plugins/ainterlinked.js` | push | docs (plugin API from `@opencode-ai/plugin` 1.4.17 types) |
| Qwen Code | `~/.qwen/settings.json` | same file (Claude-compatible) | turn | docs |
| Windsurf | `~/.codeium/windsurf/mcp_config.json` | none | pull | docs |
| Zed | `settings.json` `context_servers` (`%APPDATA%\Zed` on Windows, `~/.config/zed` elsewhere) | none | pull | docs |
| Kiro | `~/.kiro/settings/mcp.json` (tools auto-approved) | none | pull | docs |
| Cline | `cline_mcp_settings.json` (VS Code globalStorage or `~/.cline`) | none | pull | docs |
| Crush | `~/.config/crush/crush.json` `mcp` | none | pull | docs |

## How each hook dialect is answered

Every hook is invoked as `ainterlinked hook <harness> <NativeEventName>` and reads the harness's JSON payload on stdin.

| Dialect | Context injection | Keep going on stop | Deny an edit |
|---|---|---|---|
| Claude Code, Codex, Qwen, Devin | `hookSpecificOutput.additionalContext` on SessionStart, UserPromptSubmit and PostToolUse | `{"decision":"block","reason":…}` on Stop | `hookSpecificOutput.permissionDecision:"deny"` on PreToolUse |
| Copilot (camelCase events) | top-level `additionalContext` on sessionStart and postToolUse | `{"decision":"block"}` on agentStop (the CLI caps this at 8 in a row) | `permissionDecision:"deny"` on preToolUse |
| Cursor | `additional_context` on sessionStart and postToolUse | `followup_message` on stop (`loop_limit` 5) | `permission:"deny"` on preToolUse |
| Gemini CLI | `hookSpecificOutput.additionalContext` on SessionStart, BeforeAgent and AfterTool | `{"decision":"deny"}` on AfterAgent, which retries with the reason as the next prompt | `{"decision":"deny"}` on BeforeTool |
| OpenCode plugin | appended to the tool output in `tool.execute.after` | `client.session.promptAsync` when the session is idle, polled every 3 s | throws in `tool.execute.before` |

Messages are only taken from the inbox, and so marked read, on events whose output the harness actually shows the model. Copilot's `userPromptSubmitted` and Cursor's `beforeSubmitPrompt` ignore hook output, so on those events mail waits for the next event that can carry it.

## Caveats per harness

- **Claude Code.** `~/.claude.json` is rewritten by running sessions, so the MCP entry goes through the official `claude mcp` CLI. If the CLI is not on PATH, the installer edits the file directly. Push into idle sessions uses the `claude/channel` research preview. It needs `claude --dangerously-load-development-channels server:ainterlinked`, which `ainterlinked launch claude` adds for you. Claude asks you to confirm the development channel when it starts.
- **Codex.** New or changed hooks are skipped until you trust them. Open Codex and run `/hooks` once after installing. The MCP tool timeout is raised to 660 s so `wait_for_messages` can long-poll.
- **Copilot.** One hooks file serves both the CLI and VS Code agent hooks. VS Code also reads `~/.copilot/mcp-config.json`, and that is harmless: both servers resolve to the same agent because they share the parent process.
- **Cursor.** On Windows, Cursor runs hook commands through PowerShell, so a binary path that contains spaces is written as `& 'C:/…/ainterlinked.exe'`. Community reports say `followup_message` is unreliable on some Windows builds.
- **Gemini CLI.** Hook timeouts are in milliseconds (15000).
- **OpenCode.** Both `plugin/` and `plugins/` are scanned. The installer writes to `plugins/`.
- **Pull-only harnesses.** The agent sees new mail when it calls `read_inbox` or `wait_for_messages`. The MCP server instructions tell the model to do this.

## Identity across processes

A harness session usually produces several processes: one MCP server and many short-lived hook processes. They agree on one agent through the **anchor process**, the nearest ancestor that is not a shell or launcher (`sh`, `bash`, `cmd`, `powershell`, `uv`, `npx` and similar). The agent key is `harness family + machine + anchor PID`, so:

- the Claude Code MCP server and its hooks are one agent, tested live;
- Copilot's `vscode` MCP server and `copilot` hooks are one agent, because they belong to the same family;
- Cline and Copilot in the same VS Code process are two agents;
- a resumed session with the same `session_id` revives its old identity and keeps its unread mail.

Set `AINTERLINKED_ANCHOR_PID` to override detection for unusual launchers.
