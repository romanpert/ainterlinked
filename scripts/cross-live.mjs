// Cross-harness live test: a real Claude Code and a real Codex work in the
// same project over ainterlinked, and the run is recorded for demos.
//
//   node scripts/cross-live.mjs [codex] [ainterlinked]   (CROSS_OUT=dir for the material)
//
// One bounded run (hard limit below), on an isolated hub (a temporary
// AINTERLINKED_HOME), so the dashboard only shows these two agents. It spends
// one short Claude Haiku session from the Claude plan and one Codex session
// from the ChatGPT plan; no API keys, no per-token billing.
//
// The story the checks verify:
//  1. Claude reserves cart.js for task #1 (which claims the task).
//  2. Codex tries to claim #1: refused, the claim is atomic.
//  3. Codex pings Claude, Claude answers (messages both ways).
//  4. Codex tries to edit cart.js: its PreToolUse hook denies it.
//  5. Codex queues for cart.js; Claude fixes the bug and closes #1, which
//     hands cart.js to Codex, which then adds its comment.
// Codex only runs hooks the user approved in Codex (/hooks); this script
// never bypasses that.
import { spawn, spawnSync, execFileSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { createRequire } from "node:module";
import { appendFileSync, copyFileSync, createWriteStream, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { DatabaseSync } from "node:sqlite";
import { homedir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const HERE = dirname(fileURLToPath(import.meta.url));
const NPM = join(homedir(), "AppData/Roaming/npm/node_modules/@openai/codex/node_modules/@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe");
const CODEX = process.argv[2] ?? NPM;
const IL = process.argv[3] ?? join(homedir(), "go/bin/ainterlinked.exe");
const DRY = process.env.CROSS_DRY === "1";
const HARD_LIMIT_MS = DRY ? 15_000 : 9 * 60 * 1000;
const PORT = 7790 + Math.floor(Math.random() * 100);

const stamp = new Date().toISOString().replace(/[:T]/g, "-").slice(0, 19);
const OUT = process.env.CROSS_OUT ?? join(HERE, "..", "..", "ainterlinked-media", `cross-live-${stamp}`);
const ROOT = join(process.platform === "win32" ? "C:/tmp" : "/tmp", `il-cross-${stamp}`);
const HOME = join(ROOT, "hub");
const PROJECT = join(ROOT, "shop");
mkdirSync(OUT, { recursive: true });
mkdirSync(HOME, { recursive: true });
mkdirSync(PROJECT, { recursive: true });
const c = randomBytes(2).toString("hex").toUpperCase();
const env = { ...process.env, AINTERLINKED_HOME: HOME };

const t0 = Date.now();
const deadline = t0 + HARD_LIMIT_MS;
const stampLine = (s) => `[${((Date.now() - t0) / 1000).toFixed(1).padStart(6)}s] ${s}`;
const say = (s) => { const l = stampLine(s); console.log(l); appendFileSync(join(OUT, "timeline.txt"), l + "\n"); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const il = (...a) => spawnSync(IL, a, { windowsHide: true, env, encoding: "utf8" }).stdout ?? "";

// --- the project: a tiny cart with a float rounding bug ---------------------
writeFileSync(join(HOME, "config.toml"), 'machine = "workstation"\n');
writeFileSync(join(PROJECT, "cart.js"), [
  "// Shopping cart helpers.",
  "function total(items) {",
  "  return items.reduce((sum, item) => sum + item.price * item.qty, 0);",
  "}",
  "",
  "module.exports = { total };",
  "",
].join("\n"));
writeFileSync(join(PROJECT, "README.md"), "# shop\n\nTiny demo project for ainterlinked.\n");
// The owner's covenant: big enough that area rules are shown when an agent
// works there (not all in the briefing), with one rule whose effect is visible.
mkdirSync(join(PROJECT, ".ainterlinked"), { recursive: true });
writeFileSync(join(PROJECT, ".ainterlinked", "covenant.md"), [
  "# Covenant of shop",
  "## *",
  "- Keep changes small and focused on the task you hold.",
  "## cart.js",
  "- Money is rounded to cents with Math.round(x * 100) / 100, never toFixed.",
  "- When you report a change to cart.js (a task result or a message), end the report with the tag [covenant:cart].",
  "## api/**",
  "- Handlers validate input at the boundary and never trust client prices.",
  "## db/migrations/**",
  "- Never edit a merged migration; add a new one with its down step.",
  "## ui/**",
  "- Use the design tokens for colors; never hardcode hex values in components.",
  "## package.json",
  "- New dependencies need a one-line reason in the commit message and the owner's OK.",
  "## infra/**, .github/**",
  "- Ask the owner before changing CI, deployment or anything that costs money.",
  "## tests/**",
  "- Tests are deterministic: no network, no sleeps, no real clock.",
  "## .ainterlinked/covenant.md",
  "- Propose changes to the owner; don't edit the covenant yourself.",
  "",
].join("\n"));
const git = (...a) => spawnSync("git", ["-c", "user.name=demo", "-c", "user.email=demo@example.invalid", ...a], { windowsHide: true, cwd: PROJECT, encoding: "utf8" });
git("init", "-q", "-b", "main");
git("add", ".");
git("commit", "-q", "-m", "init");
il("task", "add", "Fix rounding in cart total()", "--board", "shop", "--prio", "1");

// --- the hub, read-only, for the checks -------------------------------------
const q = (sql, ...p) => {
  if (!existsSync(join(HOME, "hub.db"))) return [];
  const db = new DatabaseSync(join(HOME, "hub.db"), { readOnly: true });
  try { return db.prepare(sql).all(...p); } finally { db.close(); }
};
const waitFor = async (fn, ms = 1000, until = deadline) => {
  while (Date.now() < until) {
    const v = fn();
    if (v) return v;
    await sleep(ms);
  }
  return null;
};

// --- dashboard + recording ----------------------------------------------------
const procs = [];
const serve = spawn(IL, ["serve", "--listen", `127.0.0.1:${PORT}`], { windowsHide: true, env, stdio: "ignore" });
procs.push(serve);
const up = async () => { try { return (await fetch(`http://127.0.0.1:${PORT}/`)).ok; } catch { return false; } };
for (const end = Date.now() + 10_000; Date.now() < end && !(await up());) await sleep(200);
const STOP = join(ROOT, "stop-recording");
const recorder = spawn("python", ["-I", join(HERE, "record_dashboard.py"), `http://127.0.0.1:${PORT}/`, OUT, STOP, String(HARD_LIMIT_MS / 1000 + 30)], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
let recLog = "";
recorder.stdout.on("data", (d) => (recLog += d));
recorder.stderr.on("data", (d) => (recLog += d));
const recorderDone = new Promise((r) => recorder.on("exit", r));
procs.push(recorder);

// The activity feed as a terminal recording (asciicast v2, plays with asciinema).
const cast = createWriteStream(join(OUT, "feed.cast"));
cast.write(JSON.stringify({ version: 2, width: 110, height: 30, timestamp: Math.floor(t0 / 1000), title: "ainterlinked feed -f" }) + "\n");
const feed = spawn(IL, ["feed", "-f", "-n", "50"], { windowsHide: true, env, stdio: ["ignore", "pipe", "ignore"] });
feed.stdout.on("data", (d) => cast.write(JSON.stringify([(Date.now() - t0) / 1000, d.toString().replace(/\r?\n/g, "\r\n")]) + "\n"));
procs.push(feed);

// --- Claude Code ------------------------------------------------------------
const cmd = IL.replaceAll("\\", "/");
const mcpConfig = join(HOME, "claude-mcp.json");
writeFileSync(mcpConfig, JSON.stringify({ mcpServers: { ainterlinked: { type: "stdio", command: IL, args: ["mcp", "--harness", "claude-code"] } } }));
const hook = (ev, matcher) => ({ ...(matcher ? { matcher } : {}), hooks: [{ type: "command", command: `${cmd} hook claude-code ${ev}`, timeout: 15 }] });
const settings = join(HOME, "claude-settings.json");
writeFileSync(settings, JSON.stringify({
  permissions: { allow: ["mcp__ainterlinked", "Read", "Edit"] },
  hooks: {
    SessionStart: [hook("SessionStart")], UserPromptSubmit: [hook("UserPromptSubmit")],
    PreToolUse: [hook("PreToolUse", "Edit|Write")], PostToolUse: [hook("PostToolUse")],
    Stop: [hook("Stop")], SessionEnd: [hook("SessionEnd")],
  },
}));
const claudePrompt = [
  "You are one of two AI agents in a live demo of ainterlinked, a bus between AI coding agents. The other agent is Codex. Use the ainterlinked MCP tools. Do exactly this:",
  "1. Call whoami.",
  "2. Call reserve_files with paths [\"cart.js\"], reason \"fixing rounding in total()\" and task 1. This also claims task 1.",
  "3. Call wait_for_messages with timeout_seconds 120 (call it again whenever it returns no messages). When a message containing PING arrives, answer it with send_message, reply_to set to its id, body: the same code with PING replaced by PONG, plus one short sentence saying you are fixing the rounding in cart.js.",
  "4. Keep waiting the same way until a message containing QUEUED arrives. Only then fix total() in cart.js with the Edit tool so it returns the sum rounded to cents: Math.round(sum * 100) / 100.",
  "5. Call update_task with id 1, status done and a one-sentence result describing your change. That hands cart.js to the next agent in line.",
  "6. Wait once more (wait_for_messages, timeout_seconds 120) for a message containing DONE, then finish with a one-line summary.",
  "Never run shell commands. Never edit any file other than cart.js.",
].join("\n");
const claudeBin = process.platform === "win32"
  ? execFileSync("where.exe", ["claude"], { windowsHide: true, encoding: "utf8" }).split(/\r?\n/).find((l) => l.toLowerCase().endsWith(".exe"))
  : "claude";
const claudeLog = createWriteStream(join(OUT, "claude.jsonl"));
say(DRY ? "dry run: no agents" : "starting Claude Code (haiku)");
const claude = spawn(DRY ? process.execPath : claudeBin, DRY ? ["-e", "setTimeout(() => {}, 8000)"] : [
  "-p", claudePrompt, "--model", "haiku", "--max-turns", "30",
  "--output-format", "stream-json", "--verbose",
  "--setting-sources", "project", "--settings", settings,
  "--strict-mcp-config", "--mcp-config", mcpConfig,
  "--allowedTools", "mcp__ainterlinked", "Read", "Edit",
], { windowsHide: true, env, cwd: PROJECT, stdio: ["ignore", "pipe", "pipe"] });
claude.stdout.pipe(claudeLog);
claude.stderr.on("data", (d) => claudeLog.write(d));
const claudeDone = new Promise((r) => claude.on("exit", r));
procs.push(claude);

const claudeReady = await waitFor(() => q(
  "SELECT l.agent_name FROM locks l JOIN tasks t ON t.assignee = l.agent_name WHERE l.pattern = 'cart.js' AND t.id = 1 AND l.agent_name LIKE 'claude@%'")[0], 1000, Date.now() + (DRY ? 3000 : 180_000));
say(claudeReady ? `${claudeReady.agent_name} reserved cart.js and holds task #1` : "Claude did not reserve cart.js in time");

// --- Codex --------------------------------------------------------------------
const codexPrompt = [
  "You are one of two AI agents in a live demo of ainterlinked, a bus between AI coding agents. The other agent is Claude Code (its name starts with \"claude@\"). Use the ainterlinked MCP tools. Do exactly this:",
  "1. Call whoami, then list_agents to find the Claude agent's name.",
  "2. Call claim_task with id 1. It may already be taken: that is expected; note who has it.",
  `3. send_message to the Claude agent: "PING-${c}: hi, I'm Codex. I see task #1 is yours. Reply with PONG-${c}."`,
  "4. Try to add the line \"// Reviewed by Codex\" as the first line of cart.js using apply_patch (never use a shell command to edit files). If the edit is refused, don't retry and don't work around it.",
  "5. Call reserve_files with paths [\"cart.js\"] and wait true, so cart.js becomes yours when Claude lets go.",
  `6. send_message to the Claude agent: "QUEUED-${c}: I'm queued for cart.js. It hands over to me when you close #1."`,
  "7. Call wait_for_messages with timeout_seconds 240, repeatedly, until you are told cart.js is now yours.",
  `8. Then, with apply_patch, add this line right above \"function total\" in cart.js: "/** Sum of price x qty, rounded to cents. Reviewed by Codex (REVIEWED-${c}). */"`,
  `9. Call release_files, then send_message to the Claude agent a message that starts with "DONE-${c}:" and reports your change to cart.js in one sentence, and finish.`,
  "Do not run shell commands.",
].join("\n");
let codex = null;
let codexDone = Promise.resolve(null);
if (claudeReady) {
  say("starting Codex (its configured default model)");
  const codexLog = createWriteStream(join(OUT, "codex.jsonl"));
  codex = spawn(CODEX, ["exec", "--json", "--skip-git-repo-check", "--ephemeral", "-s", "workspace-write", "-C", PROJECT,
    "-c", `mcp_servers.ainterlinked.env.AINTERLINKED_HOME="${HOME.replaceAll("\\", "/")}"`, codexPrompt],
  { windowsHide: true, env, stdio: ["ignore", "pipe", "pipe"] });
  codex.stdout.pipe(codexLog);
  codex.stderr.on("data", (d) => codexLog.write(d));
  codexDone = new Promise((r) => codex.on("exit", r));
  procs.push(codex);
}

// Narrate the run from the hub's event log.
let lastEvent = 0;
const narrator = setInterval(() => {
  for (const e of q("SELECT id, kind, summary FROM events WHERE id > ? ORDER BY id", lastEvent)) {
    lastEvent = e.id;
    say(`${e.kind.padEnd(6)} ${e.summary}`);
  }
}, 1000);
const killer = setTimeout(() => { say("hard limit reached, stopping the agents"); claude.kill(); codex?.kill(); }, Math.max(0, deadline - Date.now()));
await Promise.all([claudeDone, codexDone]);
clearTimeout(killer);
await sleep(2500);
clearInterval(narrator);
say("both agents finished");

// --- checks ---------------------------------------------------------------------
let failures = 0;
const results = [];
const ok = (cond, what, extra = "") => {
  const line = `${cond ? "PASS" : "FAIL"}  ${what}${extra ? "  — " + extra : ""}`;
  results.push(line);
  if (!cond) failures++;
};
const agents = q("SELECT name, harness FROM agents WHERE project = 'shop'");
const claudeName = agents.find((a) => a.harness === "claude-code")?.name;
const codexName = agents.find((a) => a.harness === "codex")?.name;
ok(Boolean(claudeName && codexName), "Claude Code and Codex joined the same project", agents.map((a) => `${a.name} [${a.harness}]`).join(", "));
const msgs = q("SELECT id, sender_name, target, body, created_at FROM messages ORDER BY id");
const ping = msgs.find((m) => m.sender_name === codexName && m.body.includes(`PING-${c}`));
const pong = msgs.find((m) => m.sender_name === claudeName && m.body.includes(`PONG-${c}`));
ok(Boolean(ping), "Codex → Claude message", ping?.body.slice(0, 80));
ok(Boolean(pong), "Claude → Codex reply", pong && ping ? `${((pong.created_at - ping.created_at) / 1000).toFixed(1)} s after the ping` : "");
const task = q("SELECT status, assignee FROM tasks WHERE id = 1")[0];
const claims = q("SELECT summary FROM events WHERE kind = 'task' AND summary LIKE '%claimed #1%'");
const codexText = existsSync(join(OUT, "codex.jsonl")) ? readFileSync(join(OUT, "codex.jsonl"), "utf8") : "";
ok(task?.assignee === claudeName && claims.length === 1 && /claim_task/.test(codexText), "task #1 claimed exactly once; Codex's claim refused",
  `assignee ${task?.assignee}, status ${task?.status}, ${claims.length} claim event(s)`);
const denied = /is reserved by claude@/.test(codexText);
ok(denied, "Codex's edit of cart.js was denied by its PreToolUse hook");
const queued = q("SELECT summary FROM events WHERE kind = 'lock' AND summary LIKE ? ", `${codexName} queued for cart.js`).length > 0;
const handed = q("SELECT summary FROM events WHERE kind = 'lock' AND summary LIKE ?", `${codexName} got cart.js from the queue`).length > 0;
ok(queued && handed, "Codex queued for cart.js and got it when Claude closed #1");
let fixed = false;
let reviewed = false;
try {
  const cart = readFileSync(join(PROJECT, "cart.js"), "utf8");
  reviewed = cart.includes(`REVIEWED-${c}`);
  copyFileSync(join(PROJECT, "cart.js"), join(OUT, "cart.final.js"));
  fixed = createRequire(join(PROJECT, "x.js"))("./cart.js").total([{ price: 0.1, qty: 1 }, { price: 0.2, qty: 1 }]) === 0.3;
} catch (e) {
  results.push("cart.js: " + e.message);
}
ok(fixed, "Claude's fix works: total(0.1 + 0.2) === 0.3");
ok(reviewed, "Codex's comment landed after the hand-over");
const done = msgs.find((m) => m.sender_name === codexName && m.body.includes(`DONE-${c}`));
ok(Boolean(done), "Codex told Claude it was done");
// The covenant: neither prompt mentions the tag; only the repo's rules do.
const claudeText = existsSync(join(OUT, "claude.jsonl")) ? readFileSync(join(OUT, "claude.jsonl"), "utf8") : "";
// How the cart.js rules reached each agent: the whole covenant in the
// briefing, as context after touching the file, or an edit held back once.
// The hooks log every delivery to <home>/covenant.log.
const deliveries = existsSync(join(HOME, "covenant.log"))
  ? readFileSync(join(HOME, "covenant.log"), "utf8").trim().split("\n").filter(Boolean).map((l) => JSON.parse(l)) : [];
writeFileSync(join(OUT, "covenant.log"), deliveries.map((d) => JSON.stringify(d)).join("\n") + "\n");
const ids = Object.fromEntries(q("SELECT id, harness FROM agents").map((a) => [a.id, a.harness]));
const route = (harness) => deliveries.filter((d) => ids[d.agent] === harness && d.areas.includes("cart.js")).map((d) => d.route);
const claudeVia = route("claude-code");
const codexVia = route("codex");
ok(claudeVia.length > 0, "the covenant's cart.js rules reached Claude", claudeVia.join(" + "));
ok(codexVia.length > 0, "the covenant's cart.js rules reached Codex", codexVia.join(" + "));
const result = q("SELECT result FROM tasks WHERE id = 1")[0]?.result ?? "";
ok(result.includes("[covenant:cart]"), "Claude followed the covenant (tag in its task result)", result.slice(0, 90));
ok(Boolean(done?.body.includes("[covenant:cart]")), "Codex followed the covenant (tag in its report)", done?.body.slice(0, 90));

// --- material ---------------------------------------------------------------------
writeFileSync(join(OUT, "messages.txt"), msgs.map((m) => `#${m.id} ${new Date(m.created_at).toISOString().slice(11, 19)} ${m.sender_name} → ${m.target}\n${m.body}\n`).join("\n"));
writeFileSync(join(OUT, "final-state.txt"), ["agents --all", il("agents", "--all"), "tasks --board shop", il("tasks", "--board", "shop"), "feed -n 100", il("feed", "-n", "100")].join("\n\n"));
writeFileSync(join(STOP), "");
await Promise.race([recorderDone, sleep(30_000)]);
feed.kill();
serve.kill();
cast.end();
let video = "";
if (existsSync(join(OUT, "dashboard.webm"))) {
  const ff = spawnSync("ffmpeg", ["-y", "-loglevel", "error", "-i", join(OUT, "dashboard.webm"), "-c:v", "libx264", "-pix_fmt", "yuv420p", "-movflags", "+faststart", join(OUT, "dashboard.mp4")]);
  video = ff.status === 0 ? "dashboard.mp4 + dashboard.webm" : "dashboard.webm (mp4 conversion failed)";
} else {
  results.push("recording: " + recLog.trim().split("\n").slice(-3).join(" | "));
}
const summary = [
  `# ainterlinked cross-harness live run, ${stamp}`,
  "",
  `Claude Code (haiku) and Codex on one isolated hub, project "shop", code ${c}. Total ${((Date.now() - t0) / 1000).toFixed(0)} s.`,
  "",
  ...results.map((r) => "- " + r),
  "",
  "Files: timeline.txt (narrated hub events), messages.txt, claude.jsonl / codex.jsonl (full transcripts), feed.cast (asciinema),",
  `final-state.txt, cart.final.js, ${video || "no video"}, dashboard-final-{dark,light}.png.`,
].join("\n");
writeFileSync(join(OUT, "SUMMARY.md"), summary + "\n");
for (const p of procs) if (p.exitCode === null) p.kill();
console.log("\n" + results.join("\n"));
console.log(`\nmaterial: ${OUT}`);
if (!failures) rmSync(ROOT, { recursive: true, force: true });
else console.log("kept for debugging:", ROOT);
console.log(failures ? `\n${failures} failure(s)` : "\nall checks passed");
process.exit(failures ? 1 : 0);
