// Live test of waking an idle Codex thread (delivery.wake_idle). It uses your
// Codex plan: a thread of at most three short turns, with a hard time limit.
// The bus is isolated (a temporary AINTERLINKED_HOME, passed to Codex's MCP
// server and hooks); Codex itself is yours, with its approved hooks, so the
// thread shows in your Codex history until it is archived at the end.
//
// Codex runs as the IDE extension runs it: `codex app-server` on stdio, one
// thread, one turn from this script. The hub then sees the thread idle and,
// for mail addressed to it by name, runs `codex queue --thread <id>`. That
// only queues the text: this script records whether the app server starts
// the turn by itself, and if it doesn't, whether a client that asks it to
// (thread/queue/start, as an app may on thread/queue/changed) gets it done.
//
//   node scripts/codex-wake-live.mjs [codex] [ainterlinked]   (CODEX_MODEL=name)
import { spawn, spawnSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const win = process.platform === "win32";
const NPM = join(homedir(), "AppData/Roaming/npm/node_modules/@openai/codex/node_modules/@openai/codex-win32-x64/vendor/x86_64-pc-windows-msvc/bin/codex.exe");
const CODEX = process.argv[2] ?? (win ? NPM : "codex");
const IL = process.argv[3] ?? join(homedir(), win ? "go/bin/ainterlinked.exe" : "go/bin/ainterlinked");
const LIMIT_MS = 5 * 60 * 1000;
const HOME = mkdtempSync(join(tmpdir(), "il-cxwake-"));
const PROJECT = mkdtempSync(join(tmpdir(), "cxwake-demo-"));
const env = { ...process.env, AINTERLINKED_HOME: HOME };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
writeFileSync(join(HOME, "config.toml"), "[delivery]\nwake_idle = true\n\n[limits]\nmax_holds = 2\n");
const t0 = Date.now();
const say = (s) => console.log(`[${((Date.now() - t0) / 1000).toFixed(1).padStart(6)}s] ${s}`);

// JSON-RPC over a child's stdio, newline-delimited.
function rpc(child, onRequest, onNote) {
  let id = 0;
  const waiting = new Map();
  createInterface({ input: child.stdout }).on("line", (l) => {
    let m;
    try { m = JSON.parse(l); } catch { return; }
    if (m.id !== undefined && m.method) return onRequest(m);
    if (m.id !== undefined) return waiting.get(String(m.id))?.(m);
    onNote?.(m);
  });
  const dead = { error: { message: "the process is gone" } };
  let alive = true;
  const gone = () => { alive = false; for (const [, r] of waiting) r(dead); waiting.clear(); };
  child.on("exit", gone);
  child.on("error", gone);
  const send = (m) => { if (alive) child.stdin.write(JSON.stringify({ jsonrpc: "2.0", ...m }) + "\n"); };
  return {
    send,
    call: (method, params) => new Promise((res) => {
      if (!alive) return res(dead);
      const i = ++id;
      waiting.set(String(i), res);
      send({ id: i, method, params });
    }),
  };
}

// Peer agent.
const peer = spawn(IL, ["mcp", "--harness", "cursor"], { windowsHide: true, env, cwd: PROJECT, stdio: ["pipe", "pipe", "inherit"] });
const p = rpc(peer, (m) => p.send({ id: m.id, result: { roots: [] } }));
const tool = async (name, args = {}) => (await p.call("tools/call", { name, arguments: args })).result.content[0].text;
await p.call("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "peer", version: "1" } });
p.send({ method: "notifications/initialized" });
console.log("peer:", (await tool("whoami")).split("\n")[0]);

// Codex, as the IDE runs it.
const home = HOME.replaceAll("\\", "/");
const cx = spawn(CODEX, ["-c", `mcp_servers.ainterlinked.env.AINTERLINKED_HOME="${home}"`, "app-server"], { windowsHide: true, env, cwd: PROJECT, stdio: ["pipe", "pipe", "pipe"] });
let err = "", gone = false;
cx.stderr.on("data", (d) => (err += d));
cx.on("error", (e) => { err += e; gone = true; });
cx.on("exit", (c) => { gone = true; say(`codex exited ${c}`); });
const limit = setTimeout(() => { say("time limit: ending codex"); cx.kill(); }, LIMIT_MS);
const notes = [];
let turnsDone = 0, turnsStarted = 0;
const c = rpc(cx, (m) => {
  // Only the bus's own tools are let through; nothing else runs in this test.
  const ours = m.method === "mcpServer/elicitation/request" && m.params?.serverName === "ainterlinked";
  say(`codex asks ${m.method}${ours ? " (ainterlinked: accepted)" : " (declined)"}`);
  if (m.method === "mcpServer/elicitation/request") return c.send({ id: m.id, result: { action: ours ? "accept" : "decline" } });
  if (/requestApproval|Approval$/.test(m.method)) return c.send({ id: m.id, result: { decision: "decline" } });
  c.send({ id: m.id, error: { code: -32601, message: "not supported by this test" } });
}, (m) => {
  if (/delta|tokenUsage|item\/(started|updated)/.test(m.method)) return;
  notes.push(m.method);
  if (m.method === "turn/started") { turnsStarted++; say("turn started"); }
  if (m.method === "turn/completed") { turnsDone++; say(`turn ${turnsDone} completed: ${m.params?.turn?.status ?? ""}`); }
  if (/^thread\/queue|^thread\/status/.test(m.method)) say(`${m.method} ${JSON.stringify(m.params).slice(0, 160)}`);
});

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

let thread = "";
const init = await c.call("initialize", { clientInfo: { name: "wake-live", title: "ainterlinked wake test", version: "1" } });
if (init.error) say("initialize: " + JSON.stringify(init.error));
c.send({ method: "initialized" });
const started = await c.call("thread/start", { cwd: PROJECT, approvalPolicy: "never", sandbox: "read-only", ...(process.env.CODEX_MODEL ? { model: process.env.CODEX_MODEL } : {}) });
thread = started.result?.thread?.id ?? "";
say(`thread ${thread || JSON.stringify(started.error)}`);
if (thread) {
  await c.call("turn/start", { threadId: thread, input: [{ type: "text", text: "You are connected to ainterlinked, a bus shared with other AI agents: when one asks you something in a message, do it with the ainterlinked tools. There is nothing to do yet: call the ainterlinked tool whoami, then say READY and stop. Don't run commands or edit files." }] });
  check("the first turn ended", await until(() => turnsDone >= 1, 120_000));
  const name = await until(async () => (await tool("list_agents")).match(/(codex@[^\s"]+)/)?.[1], 20_000);
  await sleep(8000);
  const state = (await tool("list_agents")).split("\n").find((l) => name && l.includes(name)) ?? "";
  check(`it is on the bus and idle (${state.trim().slice(0, 70)})`, / idle/.test(state));
  if (name) {
    const n = turnsDone, sent = Date.now();
    await ask(name, "PINEAPPLE");
    const woke = await replied("PINEAPPLE", 60_000);
    check(`a message by name woke it by itself (${woke ? ((Date.now() - sent) / 1000).toFixed(0) + " s" : "no reply in 60 s"})`, woke);
    if (!woke && !gone) {
      const q = await c.call("thread/queue/list", { threadId: thread });
      say(`queued for the thread: ${JSON.stringify(q.result?.data?.map((s) => s.input?.[0]?.text?.slice(0, 60)) ?? q.error)}`);
      if (turnsStarted === n && q.result?.data?.length) {
        const s = await c.call("thread/queue/start", { threadId: thread });
        say(`thread/queue/start: ${s.error ? JSON.stringify(s.error) : "turn " + (s.result?.turn?.id ?? "?")}`);
        console.log(`INFO  with a client that starts the queue it replied: ${Boolean(await replied("PINEAPPLE", 90_000))}`);
      }
    }
    await until(() => turnsDone > n || turnsStarted === n, 60_000);
  }
}
console.log(`notifications: ${[...new Set(notes)].join(", ")}`);
if (thread && !gone) {
  const a = await c.call("thread/archive", { threadId: thread });
  say(`archived: ${a.error ? JSON.stringify(a.error) : "yes"}`);
}
cx.stdin.end();
await Promise.race([new Promise((r) => (gone ? r() : cx.on("exit", r))), sleep(10_000)]);
if (!gone) cx.kill();
clearTimeout(limit);
console.log("feed:\n" + (await tool("activity_feed", { limit: 30 })));
console.log("peer received:\n" + inbox);
const log = spawnSync(IL, ["hub", "status"], { windowsHide: true, env, encoding: "utf8" }).stdout ?? "";
if (err.trim()) console.log("codex's stderr:", err.trim().slice(-1500));
p.send({ method: "notifications/cancelled" });
peer.stdin.end();
await new Promise((r) => peer.on("exit", r));
const hub = log.match(/^hub process .*pid (\d+)/m)?.[1];
if (hub) { try { process.kill(Number(hub)); } catch {} await sleep(1000); }
const ok = checks.length >= 3 && checks.every(Boolean);
if (ok) {
  rmSync(HOME, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  rmSync(PROJECT, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
} else console.log("kept for debugging (the hub's log is in it):", HOME, PROJECT);
process.exit(ok ? 0 : 1);
