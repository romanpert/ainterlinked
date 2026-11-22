# Landscape (22 November 2026)

What already lets several coding agents work together, what the technical limits are, and what ainterlinked adds.

How this was checked: the projects' READMEs, docs and, for four of them, source code, read on 22 November 2026. None was installed or run. Stars are GitHub's on that day. "Not found" means not found in what was read, not that it doesn't exist.

## Short answer

- **Agents that message each other is a solved, crowded problem.** Claude Code does it natively between its own sessions, and several projects with more users than ainterlinked do it across vendors, some of them across machines.
- **Many agents in one repository without stepping on each other is less covered.** Most projects stop at messages. Some add a task board, some detect or predict conflicts, some take leases that a commit hook checks. We found no widely used project that refuses the edit itself, across vendors, before it happens.

ainterlinked is for the second problem. Messages are part of it, not the point of it.

## Messaging between sessions

| Project | Stars | What it does | Not found |
|---|---|---|---|
| Claude Code [cross-session messaging](https://code.claude.com/docs/en/cross-session-messaging) | – | Built in and on by default since August 2026. Claude sessions on one machine (and over Remote Control and the cloud) find and message each other; a message wakes an idle session. On Windows it needs 2.1.234 or later | Other vendors' agents, a task board, file reservations, broadcast |
| [Reason Network](https://github.com/reason-machines/reason-network) | 1,804 | 11 harnesses, end-to-end encrypted across machines, Windows, a hosted MCP endpoint for apps that live in the cloud. Wakes Claude Code, Codex, OpenCode, pi, Amp and Antigravity | Locks, task board |
| [agmsg](https://github.com/fujibee/agmsg) | 1,597 | Messages for about ten agent types through a skill and SQLite; wakes Codex and Claude Code; spawns agents | Locks |
| [Agent Relay](https://github.com/AgentWorkforce/relay) | 1,146 | Messaging SDK and broker for agents, also as a service | Enforced file reservations |
| [hcom](https://github.com/aannoo/hcom) | 666 | Hooks and SQLite for 14 tools, wakes idle agents, tells two agents that they edited the same file, cross-device over MQTT | Refusing the edit, task board |
| [Cotal](https://github.com/Cotal-AI/Cotal) | 407 | A mesh over NATS, with its own cloud | Enforced file reservations |
| [claude-peers-mcp](https://github.com/louislva/claude-peers-mcp) | 2,213 | Claude Code sessions find and message each other | Other vendors |
| Muse Code [session messaging](https://dev.meta.ai/docs/muse-code/session-messaging) | – | Between your own Muse Code sessions | Other vendors; Windows |

## Working on the same files

| Project | Stars | What it does | How it differs from ainterlinked |
|---|---|---|---|
| [mcp_agent_mail](https://github.com/Dicklesworthstone/mcp_agent_mail) | 2,242 | Mailboxes, advisory file leases, web UI | Deprecated in favour of the Rust port |
| [mcp_agent_mail_rust](https://github.com/Dicklesworthstone/mcp_agent_mail_rust) | 275 | The same, in Rust | Leases are advisory; a pre-commit guard checks them, after the edit |
| [Foremerge](https://github.com/naw103/foremerge) | 410 | Tells agents when their intentions conflict | Never locks, by design |
| [guild](https://github.com/mathomhaus/guild) | 297 | Go and SQLite; tasks ("quests") claimed atomically, shared memory | File reservations not found |
| [claude-squad](https://github.com/smtg-ai/claude-squad), Conductor, vibe-kanban and other worktree managers | – | One worktree per agent | No conflict while working; it moves to the merge. Agents don't know of each other |

Small projects (a few stars each) refuse edits for one or two harnesses. Prior art worth reading: agent-bus, agentclaim.

## Elsewhere

- **Claude Code [agent teams](https://code.claude.com/docs/en/agent-teams)**: a lead and its teammates with a mailbox and a shared task list. Claude only, experimental.
- **GitHub Agent HQ**: issues assigned to Copilot, Claude or Codex in the cloud. Agents don't talk to each other.
- **Grok Bot**: bots on xAI's cloud computers, in chats of two to six. It takes MCP servers as a client, but not on localhost or a private address, so it can't join a hub that stays on your machine.
- **[A2A](https://a2a-protocol.org)** and **[ACP](https://zed.dev/acp)**: protocols between agent servers, and between an editor and one agent. Coding harnesses don't expose A2A endpoints.
- **Open Duet, OpenDuet, openagents, codeg**: agents paired or orchestrated by one app, rather than sessions you started yourself in different tools.

## Hard limits today

- **MCP is pull-based.** A model only sees a message if it calls a tool or a hook injects it.
- **An idle session can be woken in some harnesses only.** Claude Code (its own messaging, `claude/channel`, or a background hook that exits with code 2), Codex (`codex queue`), OpenCode, pi and Amp (plugins), Antigravity's app (`agentapi`). Elsewhere the best is to inject at the next prompt, tool call or stop, and to hold the stop while mail waits.
- **Waking costs a turn.** Whoever pays for the session pays for it, so it needs a cap.
- **Hook contracts differ per vendor and change often.** Some events can't carry context, and a bus that isn't careful loses messages there.
- **Enforcement reaches as far as the harness's hooks.** A shell command that writes a file can be noticed but not reliably refused.
- **Prompt injection between agents is unsolved.** Framing peer messages as information and not instructions reduces the risk but doesn't remove it.

## Where ainterlinked adds value

1. **The edit is refused, not just reported.** A pre-tool hook stops an edit to a file another live agent holds, in every wired harness that has such a hook (10 of the 16), and says who holds it and how to queue. Editing reserves the file by itself; the reservation ends with the work.
2. **Queues instead of collisions.** First come, first served; the next in line gets the file and is told. Files that everyone edits a little are declared shared, so they don't become a queue: on one real day, 230 of 255 queue waits were for four such files.
3. **One board.** Tasks are claimed once, atomically, with dependencies, and go back when their holder disappears.
4. **Attention is the recipient's.** Group mail arrives as one line, long mail as a gist, and each agent sets what reaches it. Only mail by name may cost a turn.
5. **The owner's terms.** A spend cap that stops the tool calls past it, limits you can change, no telemetry, nothing listening beyond the machine. Waking idle sessions is off until you turn it on.
6. **One binary and one command** that wires 16 harnesses, with format-preserving edits, backups, `--dry-run` and a full uninstall.

## What it doesn't do, and others do

- **Other machines.** The hub is one machine's. Reason Network, hcom, Agent Relay and Cotal cross machines.
- **Agents that live in the cloud.** Grok Bot and similar need a public endpoint.
- **Everything as tested as it looks.** Several integrations are built from the vendors' docs and not yet run against the real harness: [harnesses.md](harnesses.md) says which.
- **Measured advantage.** We haven't yet compared the same work with worktrees, with another bus and with ainterlinked.

## Risks

- Vendors keep building collaboration inside their own tools. ainterlinked is useful between them, and for what they leave out.
- Any of the messaging projects above could add enforced reservations.
- Hook formats may change. They are isolated in `internal/hooks/output.go` and `internal/harness`, and covered by tests.
