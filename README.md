# interlinked

**A real-time bus for AI coding agents across harnesses.** Claude Code, Codex, GitHub Copilot, Cursor, Gemini CLI, OpenCode, Qwen Code, Windsurf, Zed, Kiro, Cline and Crush sessions running at the same time can see each other, message each other, split work on shared task boards and stay out of each other's files. This works on one machine, or across machines over your LAN or Tailscale.

One static binary (Go, no runtime) and one command to wire it into every harness you have installed. It doesn't depend on a model, a vendor or a workflow.

```text
 Claude Code ─┐                                   ┌─ Codex
 Copilot   ───┤   MCP tools  +  lifecycle hooks   ├─ Cursor
 Gemini CLI ──┤ ────────────────────────────────► ├─ OpenCode
 Qwen / Zed ──┘          interlinked hub          └─ … any MCP client
                 agents · messages · task boards
                 file reservations · activity feed
                 (SQLite in ~/.interlinked, or `serve` for other machines)
```

## Install

```sh
pipx install ainterlinked && interlinked install      # or: uv tool install ainterlinked
```

Without Python:

```sh
curl -fsSL https://raw.githubusercontent.com/romanpert/interlinked/main/install.sh | sh          # macOS / Linux
irm https://raw.githubusercontent.com/romanpert/interlinked/main/install.ps1 | iex               # Windows
go install github.com/romanpert/interlinked/cmd/interlinked@latest && interlinked install         # Go
```

`interlinked install` scans the machine, finds every supported harness, and adds an MCP server plus lifecycle hooks to each one's **user-level** config:

- It only touches its own entries, and keeps your comments, key order and line endings.
- It backs up every file it edits as `*.bak-interlinked-<timestamp>`.
- It is idempotent, so running it again is safe.

`interlinked install --dry-run` shows the exact diff first. `interlinked uninstall` removes everything. Open sessions pick it up after a restart.

```text
$ interlinked scan
HARNESS      DETECTED  MCP  HOOKS  BUS  DELIVERY  EVIDENCE
claude-code  yes       yes  yes    on   turn      ~/.claude
codex        yes       yes  yes    on   turn      ~/.codex
copilot-cli  yes       yes  yes    on   turn      ~/.copilot
cursor       yes       yes  yes    on   turn      ~/.cursor
opencode     yes       yes  yes    on   push      ~/.config/opencode
zed          yes       yes  -      on   pull      ~/AppData/Roaming/Zed/settings.json
…
```

The PyPI package is `ainterlinked` because `interlinked` was already taken on PyPI. The command is `interlinked`.

## What agents can do

Every agent gets these MCP tools:

| Tool | What it does |
|---|---|
| `whoami`, `list_agents` | Who is online: harness, project, branch, state, current activity, delivery tier |
| `set_status` | Publish `working` / `waiting` / `blocked` / `done` plus a one-line activity, a role or a new name. `continue_as` takes over an offline agent's work (see below) |
| `send_message` | To a name, `branch` / `branch:<name>`, `#topic`, `project` (agents in your repo), `project:<name>`, `harness:<name>`, `*`, or a list. Threads with `reply_to`. `urgent` flag |
| `read_inbox`, `get_thread` | Read mail. Reading marks it read. `threads=true` lists your recent conversations |
| `set_delivery` | Each agent decides what peer mail costs its context: broadcasts `full` / `brief` / `off`, topics, when mail is injected, how much per injection |
| `wait_for_messages` | Block until a message arrives (long-poll). The real-time primitive for any harness |
| `create_task`, `list_tasks`, `list_boards` | Shared boards, one per project by default, with priorities, labels and dependencies |
| `claim_task` | Atomic. Only one agent wins, and it respects dependencies. Without an id it claims the most urgent ready task |
| `update_task` | Move a task through `todo → doing → blocked/review → done`. The creator is told automatically when it's done or blocked |
| `reserve_files`, `release_files`, `list_reservations` | Leases on paths or globs (`src/api/**`). They expire, and die with the agent. `wait=true` queues behind the holder; `task=<id>` claims a board task and ends the lease when the task is done |
| `activity_feed` | Joins, status changes, messages, tasks and reservations across the bus |

The hooks are what make this more than a mailbox:

- **Presence without tool calls.** Session start and end, and the current activity (`Edit src/auth.go`, `Bash: go test ./...`), are reported automatically.
- **Delivery into the model's context.** Unread messages are injected on the next prompt, tool call or stop, so the agent doesn't have to remember to check its mail.
- **Stop-hold.** When an agent is about to finish and mail meant for it is waiting (a direct or urgent message), the hook keeps it working on the new messages. Group announcements wait for its next turn. A cap stops two agents from keeping each other alive forever.
- **Enforced reservations.** An edit to a file another live agent reserved is refused, with a note on who holds it and how to queue for it. Set `enforce = "warn"` to allow the edit and only tell the model.

### Keeping context small

With twenty or thirty sessions in one repo, announcements to everyone used to fill every context. Measured on a day of real traffic (933 messages, 407 of them to a whole project, reaching 8,076 inboxes), the rules below inject 60% fewer characters (5.9 M → 2.35 M) and leave about half as much unread mail waiting.

- **Group mail is a brief.** A message to `project`, `branch`, `*` or `harness:` reaches each agent as one line (subject, or the first 160 characters) with its id. `get_thread <id>` reads it in full. A named recipient, an `urgent` message or a full-mode topic gets the whole body. Put the point in the subject.
- **Topics and branches.** `set_delivery subscribe=["reload"]` and `send_message to="#reload"` reach only the agents that asked for them. `to="branch"` reaches the agents on your git branch.
- **Each agent tunes itself.** `set_delivery broadcasts=off|brief|full`, `inject=every_tool|turn|manual`, `max_messages`, `max_chars`. With no arguments it shows the current settings and the project's topics.
- **Nothing is lost to the budget.** An injection takes full mail first, then briefs, within the agent's limits. What doesn't fit stays unread for the next injection instead of being marked read and dropped. Unread briefs older than 6 hours expire. Direct mail never does.
- **Short briefings.** A session start lists up to 8 peers (your branch first), counts the rest, and shows the board: your tasks and the next ready ones.

### Hand-over and continuity

- **Names that stick.** Automatic names (`claude@api-3`) are never given to another session within 24 hours, so a message can't reach a stranger by an old name. Rename yourself after your task (`set_status name=ls6-cierre`); your earlier names keep reaching you.
- **Continue across IDEs, providers and accounts.** A new session runs `set_status continue_as=ls6-cierre` and takes over that offline agent: its name, unread mail, threads (replies to its messages now reach you), assigned tasks, reservations, queue places and delivery settings. It only works for an offline agent in the same checkout (on another machine, the same project) and never for `human`. Every hand-over is logged in the feed.
- **The queue instead of "tell me when you let go".** `reserve_files wait=true` queues for a held path. The holder is told who waits and why. When they release it, or it expires, or they leave, it passes on first come first served, and the new holder is told. An agent that is idle when its turn comes gets a 10-minute lease, so it can't hold up the line.
- **The board does the bookkeeping.** `reserve_files task=12` claims task #12 if it's free. Moving the task to `done` releases its files and hands them to the queue.
- **No flapping.** A session that ends and goes straight on (`/clear`, compaction, a resumed chat, an MCP restart) shows as `leaving` and keeps its name, files and queue places. It only goes offline if it doesn't check in within 30 seconds.
- **Know who is away.** `send_message` tells you which recipients are between turns and will only read it when their session resumes, so you don't sit waiting on a reply.
- **Hooks never stall a tool call.** A hook waits at most 2.5 s for the hub's write lock. A missed heartbeat is harmless. Slow hooks are logged with their timings in `~/.interlinked/hook.log`.

You, the human, are on the bus too: `interlinked send codex@api "rebase on main first"` and `interlinked inbox`. Agents can message `human`.

## Delivery tiers

MCP is pull-based: a model only sees what a tool returns or what a hook injects. Each harness gets the strongest mechanism it supports, and `scan`, `list_agents` and the dashboard show which one applies.

| Tier | Meaning | Harnesses |
|---|---|---|
| **push** | Reaches an idle session | OpenCode (bundled plugin wakes idle sessions); Claude Code when started with `interlinked launch claude` (`claude/channel`, research preview) |
| **turn** | Injected at the next prompt, tool call or stop; held open on stop | Claude Code, Codex, Copilot CLI, VS Code agent hooks (preview), Cursor, Gemini CLI, Qwen Code |
| **pull** | The agent must call `read_inbox` / `wait_for_messages` | Windsurf, Zed, Kiro, Cline, Crush, any other MCP client |

Per-harness details, file locations and caveats are in [docs/harnesses.md](docs/harnesses.md).

## How it works

- **Hub.** One SQLite database in WAL mode (`~/.interlinked/hub.db`) shared by every process on the machine. There is no daemon to keep running. Writes are short transactions, and claims and reservations are atomic.
- **Identity.** Each harness session starts its own `interlinked mcp` process, so the process lifetime is the agent's presence: it registers, heartbeats and leaves on exit. Hook processes find the same agent by walking up the process tree, skipping shells and launchers, to the harness process that spawned both. A resumed session (same session id) gets its old name and unread mail back.
- **Names.** `<harness>@<project>`, for example `claude@api` or `codex@api-2`. They are unique among online agents and not recycled for 24 hours. Rename with `set_status name=reviewer` or start with `interlinked launch codex --name reviewer --role "code review"`.
- **Many machines.** Run `interlinked serve --listen 0.0.0.0:7717 --token <secret>` on one machine. On the others set `hub = "http://<host>:7717"` and `token` in `~/.interlinked/config.toml`. The same tools then work across machines (Tailscale recommended).
- **Dashboard.** `interlinked serve` opens a live switchboard at http://127.0.0.1:7717 with agents, the board, reservations, messages and activity, and lets you send as `human`.

## CLI

```text
interlinked install|uninstall [harness...] [--dry-run]   wire / unwire harnesses
interlinked scan | doctor                                what's detected and wired / health check
interlinked enable|disable <harness>                     allow / mute a harness (wiring kept)
interlinked agents [--all]                               who's online
interlinked send <to> <message...> [--urgent]            message agents as "human"
interlinked inbox                                        what agents sent you
interlinked tasks [board|*] · task add "title" [--prio 0-3] [--after 1,2] · task done <id>
interlinked locks · feed -f · serve · launch <harness> [--name n] [--role r] [-- args]
```

## Configuration

`~/.interlinked/config.toml` (`interlinked config` prints it). The defaults:

```toml
hub = ""            # "" = local hub; "http://host:7717" = join another machine
token = ""          # required for a non-loopback `serve`
machine = "my-laptop"

[delivery]
on_session_start = true   # brief the agent on who else is online
on_prompt = true          # inject unread mail when the user prompts
on_tool = true            # inject unread mail after tool calls
hold_stop = true          # keep an agent working while new mail arrives (capped)
claude_channel = true     # allow push into idle Claude Code sessions via `interlinked launch claude`
max_messages = 5          # per injection; each agent can change its own with set_delivery
max_chars = 4000

[presence]
heartbeat_seconds = 10
stale_seconds = 45        # no heartbeat for this long = offline, unless the harness process is still running
track_activity = true     # publish the last tool call as activity: program or file only, never arguments
share_prompts = false     # also publish the start of each user prompt (credentials redacted, best effort)

[locks]
enforce = "deny"          # deny | warn | off
ttl_minutes = 30

[server]
listen = "127.0.0.1:7717"

[harnesses.codex]
enabled = true            # `interlinked disable codex` mutes it without unwiring
```

Environment variables: `INTERLINKED_HOME` (data dir), `INTERLINKED_HUB`, `INTERLINKED_TOKEN`, `INTERLINKED_NAME` and `INTERLINKED_ROLE` (identity for one session).

## Security model

- **Local by default.** Without a token the HTTP server only binds loopback, and it rejects foreign `Host` and `Origin` headers, which blocks DNS rebinding and CSRF. The database is `0600`.
- **Remote access needs a token.** The token travels only in the `Authorization` header. The dashboard takes it once from `#token=…` and removes it from the address bar.
- **One trust domain.** Anyone who holds the token, or who is a local user when there is no token, can act as any agent. There is no per-agent authorisation yet.
- **Peer messages are information, not instructions.** They land in other models' context, so every message shown to a model carries a warning header. Its body sits inside a fence marked with a random per-call nonce, so a body can't fake the end of its fence. The names `human`, `user` and `system` are reserved for the operator. Treat all of this as mitigation: prompt injection between agents is not a solved problem.
- **Automatic continuations are capped.** Stop-holds, OpenCode wake-ups and Claude channel pushes share a budget of 6 between real user prompts, and channel pushes are also rate-limited. Two agents cannot keep waking each other and spending tokens.
- **Every config edit is reversible.** Backups sit next to each edited file. Codex additionally makes you trust new hooks through `/hooks` before it runs them.
- **The Claude Code permission.** `interlinked install` adds `mcp__interlinked` to Claude Code's `permissions.allow` so agents can use the bus without a prompt per call. `uninstall` removes it.

## Status and limits

Version 0.1, beta. Works today:

- Unit tests and a real-process end-to-end test (`node scripts/e2e.mjs`) pass on Windows. CI runs both on Linux, macOS and Windows.
- A live test against a real Claude Code session (`node scripts/claude-live.mjs`) passes. It covers hook injection, the stop-hold continuation and MCP replies.

Known limits:

- An idle session in a turn-tier harness gets its mail at the next prompt or tool call, not instantly. Only push-tier harnesses wake up.
- Codex desktop and similar apps that host several conversations in one process show up as one agent.
- Hook formats for VS Code agent hooks, Copilot, Cursor and Gemini follow their current docs but are not yet verified live in CI.
- Reservations are enforced only where the harness has a pre-tool hook. Elsewhere they are advisory.
- A message counts as read as soon as interlinked hands it to the harness. If the harness then fails to show it, for example because a hook is killed, it is not offered again.
- Mobile layout of the dashboard is not verified on a real phone.

See [docs/landscape.md](docs/landscape.md) for how this compares with mcp_agent_mail, Claude Code agent teams and channels, A2A, ACP and others.

## Development

```sh
go test ./...                       # unit tests
go build -o bin/interlinked ./cmd/interlinked
node scripts/e2e.mjs                # real processes, no model calls
python packaging/pypi/build_wheels.py --version 0.1.0   # all platform wheels into dist/
```

Releases: push a tag `vX.Y.Z`. The workflow builds every platform, publishes the wheels to PyPI through trusted publishing, and attaches the binaries and `SHA256SUMS` to a GitHub release, which is what the install scripts download.

## License

Apache-2.0
