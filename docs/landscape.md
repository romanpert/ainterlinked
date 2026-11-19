# Landscape (November 2026)

The question is whether cross-harness agent collaboration already exists, what the hard technical limits are today, and where interlinked adds something.

## Short answer

The space is busy with small projects but has no dominant cross-harness solution. Each existing project covers one slice (mail, tasks or chat) for a few harnesses. Vendors are building collaboration inside their own walls:

- Claude Code has agent teams and channels.
- GitHub has Agent HQ.
- These work in the cloud or at the PR level, not between live local sessions of different vendors.

The open gap is threefold:

- reliable delivery into the model's context, with honest labels for what each harness can and cannot do;
- one data model that covers presence, mail, atomic tasks and enforced file reservations;
- an install that finds and wires every harness on the machine.

## Existing work

| Project | What it does | What it lacks for this use case |
|---|---|---|
| [mcp_agent_mail](https://github.com/Dicklesworthstone/mcp_agent_mail), and its Rust port | Agent mailboxes over MCP, a Git-backed archive, advisory file leases, a web UI. About 2k stars, active | Pull-only, with no hooks that inject mail or keep a session alive. Leases are checked late, by a pre-commit guard. No atomic task board. Wiring is manual per harness |
| Claude Code [agent teams](https://code.claude.com/docs/en/agent-teams) | A lead plus teammates with a mailbox and a shared task list | Claude only, experimental, and token-heavy |
| Claude Code [channels](https://code.claude.com/docs/en/channels-reference) | An MCP server can push events into a running session | Claude only, a research preview, and custom channels need a development flag. interlinked uses it as one delivery path |
| agent-comms (ExaDev) | MCP mesh over TCP with rooms, DMs and presence. Detects about 4 harnesses | Very small project. No tasks or reservations |
| agent-board (DL4P1N) | Shared task board MCP for several harnesses | Tasks only |
| zetbrush/multiagents, Axis, aisync, agent-comm and others | Orchestrators, file claims, claim-enforcing hooks | Each covers a slice for 2–3 harnesses |
| GitHub Agent HQ / Mission Control | Assign issues to Copilot, Claude or Codex in the cloud | Cloud and PR-centric. Agents don't talk to each other |
| claude-squad, ccmanager, Conductor, vibe-kanban | Session and worktree managers for a human | Not an agent-to-agent bus |
| [A2A](https://a2a-protocol.org) (Linux Foundation, v1.0 in March 2026) | HTTP protocol between agent servers | Coding harnesses don't expose A2A endpoints. It assumes every agent is a server |
| [Zed ACP](https://zed.dev/acp) | Protocol between an editor and one agent | Editor-to-agent, not agent-to-agent |

Star counts and activity come from secondary sources as of 2026.

## Hard limits today

- **MCP is pull-based.** A model only sees a message if it calls a tool or a hook injects it.
- **Push into an idle session exists in two places only.** Claude Code channels (preview, behind a flag) and OpenCode, whose plugin can prompt an idle session. Everywhere else the best you can do is inject at the next prompt, tool call or stop, plus holding the stop while mail is waiting.
- **Hook output contracts differ per vendor and change often.** Five dialects today: Claude-compatible, Copilot camelCase, Cursor, Gemini and the OpenCode plugin. Some events can't carry context at all, and a naive bus loses messages there. interlinked only consumes mail on events that can show it.
- **Codex requires trusting new hooks interactively** with `/hooks`.
- **Prompt injection between agents is unsolved.** Framing peer messages as information and not instructions reduces the risk but doesn't remove it.

## Where interlinked adds value

1. **Honest delivery tiers.** Every agent is labelled push, turn or pull, in the tools, the CLI and the dashboard.
2. **Messages that arrive.** Hooks inject mail into the model's context and hold the stop while new mail is waiting, with a cap against ping-pong loops. `wait_for_messages` covers everything else.
3. **One model for the four primitives.** Presence with live activity, threaded mail, task boards with atomic claims and dependencies, and file reservations that are enforced through pre-tool hooks.
4. **One-command wiring of 13 harnesses**, with format-preserving edits, backups, `--dry-run` and a full uninstall.
5. **A single static binary.** No daemon, no Python or Node at run time. SQLite WAL locally, with optional `serve` for many machines.

## Risks

- Vendors will keep building collaboration within their own ecosystems. interlinked is useful as the layer between them.
- Hook formats may change. They are isolated in `internal/hooks/output.go` and `internal/harness/defs.go`, and covered by tests.
