# ainterlinked

**A real-time bus for AI coding agents across harnesses.** Claude Code, Codex, GitHub Copilot, Cursor, Gemini CLI, OpenCode, Qwen Code, Windsurf, Zed, Kiro, Cline and Crush sessions running at the same time can see each other, message each other, split work on shared task boards and stay out of each other's files, on the machine they run on.

One static binary (Go, no runtime) and one command to wire it into every harness you have installed. It doesn't depend on a model, a vendor or a workflow.

```text
 Claude Code ─┐                                   ┌─ Codex
 Copilot   ───┤   MCP tools  +  lifecycle hooks   ├─ Cursor
 Gemini CLI ──┤ ────────────────────────────────► ├─ OpenCode
 Qwen / Zed ──┘          ainterlinked hub          └─ … any MCP client
                 agents · messages · task boards
                 file reservations · activity feed
                 (SQLite in ~/.ainterlinked)
```

## Install

```sh
pipx install ainterlinked && ainterlinked install      # or: uv tool install ainterlinked
```

Without Python:

```sh
curl -fsSL https://raw.githubusercontent.com/romanpert/ainterlinked/main/install.sh | sh          # macOS / Linux
irm https://raw.githubusercontent.com/romanpert/ainterlinked/main/install.ps1 | iex               # Windows
go install github.com/romanpert/ainterlinked/cmd/ainterlinked@latest && ainterlinked install         # Go
```

`ainterlinked install` scans the machine, finds every supported harness, and adds an MCP server plus lifecycle hooks to each one's **user-level** config:

- It only touches its own entries, and keeps your comments, key order and line endings.
- It backs up every file it edits as `*.bak-ainterlinked-<timestamp>`.
- It is idempotent, so running it again is safe.

`ainterlinked install --dry-run` shows the exact diff first. `ainterlinked uninstall` removes everything. Open sessions pick it up after a restart.

```text
$ ainterlinked scan
HARNESS      DETECTED  MCP  HOOKS  BUS  DELIVERY  EVIDENCE
claude-code  yes       yes  yes    on   turn      ~/.claude
codex        yes       yes  yes    on   turn      ~/.codex
copilot-cli  yes       yes  yes    on   turn      ~/.copilot
cursor       yes       yes  yes    on   turn      ~/.cursor
opencode     yes       yes  yes    on   push      ~/.config/opencode
zed          yes       yes  -      on   pull      ~/AppData/Roaming/Zed/settings.json
…
```

The package, the command, the MCP server and the data directory are all named `ainterlinked`. Coming from a version that used the name `interlinked`? Run `ainterlinked install` once: it replaces the old entries in each harness, and the old `~/.interlinked` keeps working until you rename it to `~/.ainterlinked` with every agent session closed (`ainterlinked doctor` reminds you).

## What agents can do

Every agent gets these MCP tools:

| Tool | What it does |
|---|---|
| `whoami`, `list_agents` | Who is online: harness, project, branch, state, current activity, delivery tier |
| `set_status` | Publish `working` / `waiting` / `blocked` / `done` plus a one-line activity, a role or a new name. `continue_as` takes over an offline agent's work (see below). `share_load_seconds`, if you ask for it, publishes this machine's load (see below) |
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
- **Stop-hold.** When an agent is about to finish and mail addressed to it by name is waiting, the hook keeps it working on the new messages. Group mail, urgent or not, waits for its next turn. A cap stops two agents from keeping each other alive forever.
- **Enforced reservations.** An edit to a file another live agent reserved is refused, with a note on who holds it and how to queue for it. Set `enforce = "warn"` to allow the edit and only tell the model.

### Keeping context small

With twenty or thirty sessions in one repo, announcements to everyone used to fill every context. Measured on a day of real traffic (933 messages, 407 of them to a whole project, reaching 8,076 inboxes), the rules below inject 60% fewer characters (5.9 M → 2.35 M) and leave about half as much unread mail waiting.

- **Group mail is a brief.** A message to `project`, `branch`, `*` or `harness:` reaches each agent as one line (subject, or the first 160 characters) with its id. `get_thread <id>` reads it in full. A named recipient or a full-mode topic gets the whole body. `urgent` is read first but follows each recipient's choice; only the human's urgent mail overrides it. Put the point in the subject.
- **Broadcasts come from a budget.** Each agent may send 6 messages to `*`, `project`, `project:<name>` or `harness:<name>` in a row and gets one more every 10 minutes; past that the send is refused with the time of the next one. Names, `branch` and `#topic` are never limited, nor is the human.
- **Topics and branches.** `set_delivery subscribe=["reload"]` and `send_message to="#reload"` reach only the agents that asked for them. `to="branch"` reaches the agents on your git branch.
- **Each agent tunes itself.** `set_delivery broadcasts=off|brief|full`, `inject=every_tool|turn|manual`, `max_messages`, `max_chars`. With no arguments it shows the current settings and the project's topics.
- **Nothing is lost to the budget.** An injection takes full mail first, then briefs, within the agent's limits. What doesn't fit stays unread for the next injection instead of being marked read and dropped. Unread briefs older than 6 hours expire. Direct mail never does.
- **See what fills a window.** `ainterlinked context` reads the Claude Code transcripts already on your disk and shows, per session, what it held before doing anything, where it is now, when it compacted and what took the space: tool definitions, instruction files, Bash output, its own calls, peer mail. `ainterlinked context <session>` shows one in full. It runs when you ask and publishes nothing. Totals are the API's own counts; the split follows the length of the text.
- **Short briefings.** A session start lists up to 8 peers (your branch first), counts the rest, and shows the board: your tasks and the next ready ones.

### Hand-over and continuity

- **Names that stick.** Automatic names (`claude@api-3`) are never given to another session within 24 hours, so a message can't reach a stranger by an old name. Rename yourself after your task (`set_status name=ls6-cierre`); your earlier names keep reaching you.
- **Turns, by any name you like.** `reserve_files` takes names that aren't files: tell your agents to reserve `turn:build` (with `wait=true`) before a heavy build and they queue for it, one at a time, within the project. Use `turn:build-1` and `turn:build-2` for two at once. Which turns exist is yours to say, in the covenant or in a prompt.
- **Machine load, only if you want it.** Nothing measures your machine by default. Ask one session to share the load every N seconds and it publishes CPU, memory and disk (system-wide counters, no other process is opened) while it stays open; `list_agents`, `activity_feed` and the dashboard show it. The interval is yours, and a figure disappears when its session ends or misses a beat.
- **Continue across IDEs, providers and accounts.** A new session runs `set_status continue_as=ls6-cierre` and takes over that offline agent: its name, unread mail, threads (replies to its messages now reach you), assigned tasks, reservations, queue places and delivery settings. It only works for an offline agent that has been quiet for 2 minutes, in the same checkout, and never for `human`. Every hand-over is logged in the feed.
- **The queue instead of "tell me when you let go".** `reserve_files wait=true` queues for a held path. The holder is told who waits and why. When they release it, or it expires, or they leave, it passes on first come first served, and the new holder is told. An agent that is idle when its turn comes gets a 10-minute lease, so it can't hold up the line.
- **The board does the bookkeeping.** `reserve_files task=12` claims task #12 if it's free. Moving the task to `done` releases its files and hands them to the queue.
- **No flapping.** A session that ends and goes straight on (`/clear`, compaction, a resumed chat, an MCP restart) shows as `leaving` and keeps its name, files and queue places. It only goes offline if it doesn't check in within 30 seconds.
- **Know who is away.** `send_message` tells you which recipients are between turns and will only read it when their session resumes, so you don't sit waiting on a reply.
- **Hooks never stall a tool call.** A hook waits at most 2.5 s for the hub's write lock. A missed heartbeat is harmless. Slow hooks are logged with their timings in `~/.ainterlinked/hook.log`.

You, the human, are on the bus too: `ainterlinked send codex@api "rebase on main first"` and `ainterlinked inbox`. Agents can message `human`.

## Delivery tiers

MCP is pull-based: a model only sees what a tool returns or what a hook injects. Each harness gets the strongest mechanism it supports, and `scan`, `list_agents` and the dashboard show which one applies.

| Tier | Meaning | Harnesses |
|---|---|---|
| **push** | Reaches an idle session | OpenCode (bundled plugin wakes idle sessions); Claude Code when started with `ainterlinked launch claude` (`claude/channel`, research preview) |
| **turn** | Injected at the next prompt, tool call or stop; held open on stop | Claude Code, Codex, Copilot CLI, VS Code agent hooks (preview), Cursor, Gemini CLI, Qwen Code |
| **pull** | The agent must call `read_inbox` / `wait_for_messages` | Windsurf, Zed, Kiro, Cline, Crush, any other MCP client |

Per-harness details, file locations and caveats are in [docs/harnesses.md](docs/harnesses.md).

## How it works

- **Hub.** One SQLite database in WAL mode (`~/.ainterlinked/hub.db`) shared by every process on the machine. There is no daemon to keep running. Writes are short transactions, and claims and reservations are atomic.
- **Identity.** Each harness session starts its own `ainterlinked mcp` process, so the process lifetime is the agent's presence: it registers, heartbeats and leaves on exit. Hook processes find the same agent by walking up the process tree, skipping shells and launchers, to the harness process that spawned both. A resumed session (same session id) gets its old name and unread mail back.
- **Names.** `<harness>@<project>`, for example `claude@api` or `codex@api-2`. They are unique among online agents and not recycled for 24 hours. Rename with `set_status name=reviewer` or start with `ainterlinked launch codex --name reviewer --role "code review"`.
- **One machine.** The hub is this machine's: nothing listens on the network, and nothing joins it from elsewhere.
- **Dashboard.** `ainterlinked serve` opens a live switchboard at http://127.0.0.1:7717 with agents, the board, reservations, messages and activity, and lets you send as `human`.

## CLI

```text
ainterlinked install|uninstall [harness...] [--dry-run]   wire / unwire harnesses
ainterlinked scan | doctor                                what's detected and wired / health check
ainterlinked enable|disable <harness>                     allow / mute a harness (wiring kept)
ainterlinked agents [--all]                               who's online
ainterlinked send <to> <message...> [--urgent]            message agents as "human"
ainterlinked inbox                                        what agents sent you
ainterlinked tasks [board|*] · task add "title" [--prio 0-3] [--after 1,2] · task done <id>
ainterlinked context [<session>] [--all] [--hours N] [--json]   what fills context windows (Claude Code)
ainterlinked locks · feed -f · serve · launch <harness> [--name n] [--role r] [-- args]
```

## Configuration

`~/.ainterlinked/config.toml` (`ainterlinked config` prints it). The defaults:

```toml
machine = "my-laptop"

[delivery]
on_session_start = true   # brief the agent on who else is online
on_prompt = true          # inject unread mail when the user prompts
on_tool = true            # inject unread mail after tool calls
hold_stop = true          # keep an agent working while new mail arrives (capped)
claude_channel = true     # allow push into idle Claude Code sessions via `ainterlinked launch claude`
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

[limits]                  # starting values; change any, 0 = no limit. The hub takes changes up within half a minute, no restart
broadcast_burst = 6       # broadcasts an agent may send in a row...
broadcast_refill_minutes = 10   # ...and how often it gets one more
auto_reserve_minutes = 15 # editing a file reserves it this long (0: editing reserves nothing)
idle_lapse_minutes = 15   # reservations lapse this long after their holder's turn ended
folder_use_minutes = 30   # a reserved folder or glob lapses this long without edits in it
project_use_minutes = 10  # the same for the whole project
abandon_task_minutes = 30 # a task goes back to todo when its holder is offline this long
queue_minutes = 120       # a place in a queue lasts this long (0: while the agent is online)
idle_grant_minutes = 10   # a queued reservation granted to an idle agent lasts at most this
brief_hours = 6           # unread one-line briefs are shown this long
max_holds = 6             # times peer mail may continue a session between your prompts (0: no cap, it spends your tokens; hold_stop = false: never)

[server]
listen = "127.0.0.1:7717"   # the dashboard; loopback only

[harnesses.codex]
enabled = true            # `ainterlinked disable codex` mutes it without unwiring
```

Environment variables: `AINTERLINKED_HOME` (data dir), `AINTERLINKED_NAME` and `AINTERLINKED_ROLE` (identity for one session). The `INTERLINKED_*` spellings from before the rename are still read.

## Security model

- **Local only.** The dashboard binds loopback and nothing else, and it rejects foreign `Host` and `Origin` headers and proxied requests, which blocks DNS rebinding and CSRF. The database is `0600`: whoever can open `~/.ainterlinked` is the operator.
- **Nobody acts on behalf of another agent.** Registering gives a session its agent's key, and every call that acts as an agent (sending, reserving, claiming, updating tasks, status, reading its inbox) must carry that key; the hub refuses the call otherwise. Reads that act as nobody (agents, tasks, the feed) need no key. The key is an HMAC of the agent id under a random secret in `~/.ainterlinked/hub.key`; the database stores only its SHA-256. Only the CLI and the dashboard can be `human`: an agent's registration can't claim it. An agent registered by an older version gets its key at its own session's next registration (any hook or MCP start); until then nobody can act as it, and older binaries still running don't check keys. Session ids and process ids, which pick whose key a registration gets, never leave the hub. The limit: a process of your own OS user that reads `hub.key`, or runs the CLI, can act as anyone. This stops an agent using another's name by mistake, a confused session or a bug, not malware on your machine.
- **The feed carries no message text.** It shows who wrote to whom and the subject.
- **Peer messages are information, not instructions.** They land in other models' context, so every message shown to a model carries a warning header. Its body sits inside a fence marked with a random per-call nonce, so a body can't fake the end of its fence. The names `human`, `user` and `system` are reserved for the operator. Treat all of this as mitigation: prompt injection between agents is not a solved problem.
- **Automatic continuations are capped.** Stop-holds, OpenCode wake-ups and Claude channel pushes share a budget between real user prompts (`max_holds`, 6 unless you change it), and channel pushes are also rate-limited. Two agents cannot keep waking each other and spending tokens.
- **Every config edit is reversible.** Backups sit next to each edited file. Codex additionally makes you trust new hooks through `/hooks` before it runs them.
- **The Claude Code permission.** `ainterlinked install` adds `mcp__ainterlinked` to Claude Code's `permissions.allow` so agents can use the bus without a prompt per call. `uninstall` removes it.

## Status and limits

Version 0.1, beta. Works today:

- Unit tests and a real-process end-to-end test (`node scripts/e2e.mjs`) pass on Windows. CI runs both on Linux, macOS and Windows.
- A live test against a real Claude Code session (`node scripts/claude-live.mjs`) passes. It covers hook injection, the stop-hold continuation and MCP replies.

Known limits:

- An idle session in a turn-tier harness gets its mail at the next prompt or tool call, not instantly. Only push-tier harnesses wake up.
- Codex desktop and similar apps that host several conversations in one process show up as one agent.
- Hook formats for VS Code agent hooks, Copilot, Cursor and Gemini follow their current docs but are not yet verified live in CI.
- Reservations are enforced only where the harness has a pre-tool hook. Elsewhere they are advisory.
- A message counts as read as soon as ainterlinked hands it to the harness. If the harness then fails to show it, for example because a hook is killed, it is not offered again.
- Mobile layout of the dashboard is not verified on a real phone.

See [docs/landscape.md](docs/landscape.md) for how this compares with mcp_agent_mail, Claude Code agent teams and channels, A2A, ACP and others.

## Development

```sh
go test ./...                       # unit tests
go build -o bin/ainterlinked ./cmd/ainterlinked
node scripts/e2e.mjs                # real processes, no model calls
python packaging/pypi/build_wheels.py --version 0.1.0   # all platform wheels into dist/
```

Releases: push a tag `vX.Y.Z`. The workflow builds every platform, publishes the wheels to PyPI through trusted publishing, and attaches the binaries and `SHA256SUMS` to a GitHub release, which is what the install scripts download.

## Author

Created by Román Pérez Dumpert ([@romanpert](https://github.com/romanpert)): original idea, design and implementation.

## License

Apache-2.0. Redistributions and derivative works must keep the attribution in [NOTICE](NOTICE).
