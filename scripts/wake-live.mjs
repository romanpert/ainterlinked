// Live test of waking an idle Claude Code session (delivery.wake_idle). It
// uses your Claude plan: one short Haiku session, with a hard time limit.
// Isolated: a temporary AINTERLINKED_HOME, --strict-mcp-config and
// --setting-sources project, so your own config is neither loaded nor touched.
//
// The session runs as the VS Code extension and the desktop app run it: open,
// stream-json both ways, without -p. This script writes to it twice (the
// first prompt and the last), so any turn between those two was started by
// the wake hook.
//
//   node scripts/wake-live.mjs [claude] [ainterlinked]   (CLAUDE_MODEL=name)
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const win = process.platform === "win32";
const CLAUDE = process.argv[2] ?? (win ? join(homedir(), ".local/bin/claude.exe") : "claude");
const IL = process.argv[3] ?? join(homedir(), win ? "go/bin/ainterlinked.exe" : "go/bin/ainterlinked");
const LIMIT_MS = 7 * 60 * 1000;
const HOME = mkdtempSync(join(tmpdir(), "il-wake-"));
const PROJECT = mkdtempSync(join(tmpdir(), "wake-demo-"));
const env = { ...process.env, AINTERLINKED_HOME: HOME };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
writeFileSync(join(HOME, "config.toml"), "[delivery]\nwake_idle = true\n\n[limits]\nmax_holds = 2\n");

const settings = join(HOME, "settings.json");
const mcpConfig = join(HOME, "mcp.json");
const hook = (event, extra = {}) => ({ type: "command", command: `${IL.replaceAll("\\", "/")} hook claude-code ${event}`, timeout: 15, ...extra });
const on = (event, matcher) => ({ ...(matcher ? { matcher } : {}), hooks: [hook(event)] });
writeFileSync(mcpConfig, JSON.stringify({ mcpServers: { ainterlinked: { type: "stdio", command: IL, args: ["mcp", "--harness", "claude-code"] } } }));
writeFileSync(settings, JSON.stringify({
  permissions: { allow: ["mcp__ainterlinked"] },
  hooks: {
    SessionStart: [on("SessionStart")],
    UserPromptSubmit: [on("UserPromptSubmit")],
    PreToolUse: [on("PreToolUse", "Edit|Write|MultiEdit|NotebookEdit")],
    PostToolUse: [on("PostToolUse")],
    Stop: [on("Stop"), { hooks: [hook("Wake", { timeout: 86400, asyncRewake: true })] }],
    SessionEnd: [on("SessionEnd")],
  },
}));

// Peer agent.
const peer = spawn(IL, ["mcp", "--harness", "cursor"], { windowsHide: true, env, cwd: PROJECT, stdio: ["pipe", "pipe", "inherit"] });
let id = 0;
const waiting = new Map();
createInterface({ input: peer.stdout }).on("line", (l) => {
  const m = JSON.parse(l);
  if (m.method === "roots/list") return peer.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: m.id, result: { roots: [] } }) + "\n");
  waiting.get(String(m.id))?.(m);
});
const call = (method, params) => new Promise((res) => { const i = ++id; waiting.set(String(i), res); peer.stdin.write(JSON.stringify({ jsonrpc: "2.0", id: i, method, params }) + "\n"); });
const tool = async (name, args = {}) => (await call("tools/call", { name, arguments: args })).result.content[0].text;
await call("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "peer", version: "1" } });
peer.stdin.write(JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" }) + "\n");
console.log("peer:", (await tool("whoami")).split("\n")[0]);

const waiters = () => {
  if (!win) return [];
  const q = "Get-CimInstance Win32_Process -Filter \"Name='ainterlinked.exe'\" | Where-Object { $_.CommandLine -match 'hook claude-code Wake' } | ForEach-Object { $_.ProcessId }";
  return spawnSync("powershell", ["-NoProfile", "-Command", q], { windowsHide: true, encoding: "utf8" }).stdout.split(/\s+/).filter(Boolean);
};
const others = new Set(waiters());

const t0 = Date.now();
const say = (s) => console.log(`[${((Date.now() - t0) / 1000).toFixed(1).padStart(6)}s] ${s}`);
const claude = spawn(CLAUDE, [
  "--output-format", "stream-json", "--verbose", "--input-format", "stream-json", "--model", process.env.CLAUDE_MODEL ?? "haiku",
  "--setting-sources", "project", "--settings", settings, "--strict-mcp-config", "--mcp-config", mcpConfig, "--allowedTools", "mcp__ainterlinked",
], { windowsHide: true, env: { ...env, CLAUDE_CODE_ENTRYPOINT: "claude-vscode" }, cwd: PROJECT, stdio: ["pipe", "pipe", "pipe"] });
let turns = 0, err = "", gone = false, model = "";
createInterface({ input: claude.stdout }).on("line", (l) => {
  let m;
  try { m = JSON.parse(l); } catch { return; }
  if (m.type === "system" && m.subtype === "init") model = m.model;
  if (m.type === "result") { turns++; say(`turn ${turns} ended: ${m.subtype} ${String(m.result ?? "").slice(0, 100).replace(/\s+/g, " ")}`); }
});
claude.stderr.on("data", (d) => (err += d));
claude.on("error", (e) => { err += e; gone = true; });
claude.on("exit", (c) => { gone = true; say(`claude exited ${c}`); });
const limit = setTimeout(() => { say("time limit: ending claude"); claude.kill(); }, LIMIT_MS);
const user = (text) => claude.stdin.write(JSON.stringify({ type: "user", message: { role: "user", content: [{ type: "text", text }] } }) + "\n");

const until = async (fn, ms) => {
  for (const end = Date.now() + ms; Date.now() < end && !gone; await sleep(500)) {
    const v = await fn();
    if (v) return v;
  }
  return null;
};
let inbox = "";
const replied = (word, ms) => until(async () => {
  const t = await tool("wait_for_messages", { timeout_seconds: 2 });
  if (!t.startsWith("No messages")) inbox += t + "\n";
  return inbox.includes(word);
}, ms);
const checks = [];
const check = (name, ok) => { checks.push(Boolean(ok)); console.log(`${ok ? "PASS" : "FAIL"}  ${name}`); };
const ask = (to, word) => tool("send_message", { to, body: `Please reply to me with the single word ${word} using send_message, then stop.` });
// settled waits for the turn after turn n to end and for the session to be idle.
const settled = async (n) => { await until(() => turns > n, 45_000); await sleep(6000); return turns; };

user("You are connected to ainterlinked, a bus shared with other AI agents: when one asks you something in a message, do it. There is nothing to do yet: say READY and stop.");
const name = await until(async () => (await tool("list_agents")).match(/(claude@[^\s"]+)/)?.[1], 60_000);
const started = name && (await until(() => turns >= 1, 90_000));
check("the session joined the bus and ended its first turn", started);
if (started) {
  await sleep(8000);
  const state = (await tool("list_agents")).split("\n").find((l) => l.includes(name)) ?? "";
  check(`it is idle (${state.trim().slice(0, 60)})`, / idle/.test(state));
  if (win) check("one waiter waits with it", waiters().filter((p) => !others.has(p)).length === 1);
  let n = turns;
  const sent = Date.now();
  await ask(name, "PINEAPPLE");
  const got = await replied("PINEAPPLE", 90_000);
  check(`a message by name woke it and it replied (${((Date.now() - sent) / 1000).toFixed(0)} s)`, got);
  if (got) {
    n = await settled(n);
    await ask("project", "KIWI");
    check("a group message didn't wake it", !(await replied("KIWI", 25_000)) && turns === n);
    await ask(name, "MANGO");
    check("a second message by name woke it again", await replied("MANGO", 90_000));
    n = await settled(n);
    await ask(name, "LIME");
    check("a third didn't: max_holds is 2 and the user hasn't spoken", !(await replied("LIME", 30_000)) && turns === n);
    user("Answer what your peers have asked you, then stop.");
    check("it was kept for the user's next prompt", await replied("LIME", 90_000));
  }
}
claude.stdin.end();
await Promise.race([new Promise((r) => (gone ? r() : claude.on("exit", r))), sleep(20_000)]);
check("it left when its app closed it", gone);
if (!gone) claude.kill();
clearTimeout(limit);
if (win) {
  await sleep(12_000);
  check("no waiter was left behind", waiters().every((p) => others.has(p)));
}
console.log(`model ${model}, ${turns} turns\nfeed:\n` + (await tool("activity_feed", { limit: 40 })));
console.log("peer received:\n" + inbox);
if (err.trim()) console.log("claude's stderr:", err.trim().slice(-1200));
peer.stdin.end();
await new Promise((r) => peer.on("exit", r));
// The isolated hub would stay for half an hour, with its files open.
const hub = spawnSync(IL, ["hub", "status"], { windowsHide: true, env, encoding: "utf8" }).stdout?.match(/^hub process .*pid (\d+)/m)?.[1];
if (hub) { try { process.kill(Number(hub)); } catch {} await sleep(1000); }
const ok = checks.length >= 8 && checks.every(Boolean);
if (ok) {
  rmSync(HOME, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  rmSync(PROJECT, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
} else console.log("kept for debugging:", HOME, PROJECT);
process.exit(ok ? 0 : 1);
