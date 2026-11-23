// Live test against a real Antigravity CLI (uses your Google account's model
// quota: one short session, with a hard time limit). Sign in to agy first and
// run `ainterlinked install antigravity`: it tests the hooks and the MCP
// server as wired in ~/.gemini/config, on an isolated hub (a temporary
// AINTERLINKED_HOME) and in a temporary project. Only ainterlinked's tools and
// edits are allowed, for this run; no permission check is skipped.
//
// A peer agent (a real `ainterlinked mcp --harness codex` process) holds
// held.txt and asks Antigravity for PINEAPPLE; when that arrives it asks for
// MANGO, which can only reach Antigravity through its hooks, and 3 s after
// that for LIME, as the turn is about to end: the Stop hook has to hold it.
// The user's prompt asks for an edit of held.txt, which must be refused, and
// one of free.txt.
//
//   node scripts/antigravity-live.mjs [agy] [ainterlinked]   (AGY_MODEL=name)
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { homedir, tmpdir } from "node:os";
import { join } from "node:path";
import { createInterface } from "node:readline";

const win = process.platform === "win32";
const AGY = process.argv[2] ?? (win ? join(homedir(), "AppData/Local/agy/bin/agy.exe") : "agy");
const IL = process.argv[3] ?? join(homedir(), win ? "go/bin/ainterlinked.exe" : "go/bin/ainterlinked");
const LIMIT_MS = 5 * 60 * 1000;
const HOME = mkdtempSync(join(tmpdir(), "il-agy-"));
const PROJECT = mkdtempSync(join(tmpdir(), "agy-demo-"));
const env = { ...process.env, AINTERLINKED_HOME: HOME };
writeFileSync(join(PROJECT, "held.txt"), "original\n");

// Peer agent.
const peer = spawn(IL, ["mcp", "--harness", "codex"], { windowsHide: true, env, cwd: PROJECT, stdio: ["pipe", "pipe", "inherit"] });
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
console.log("peer:", (await tool("reserve_files", { paths: ["held.txt"], reason: "rewriting it" })).split("\n")[1]);
await tool("set_status", { state: "working", activity: "rewriting held.txt" });

const t0 = Date.now();
const log = [];
const seen = { joined: false, pineapple: false, mango: false, lime: false };
let done = false, name = "";
const talk = (async () => {
  let asked = false;
  while (!done) {
    if (!asked) {
      name = (await tool("list_agents")).match(/(antigravity@\S+)/)?.[1] ?? "";
      if (!name) { await new Promise((r) => setTimeout(r, 300)); continue; }
      seen.joined = asked = true;
      await tool("send_message", { to: name, body: "Hi, I'm the codex agent. Please reply to me with the single word PINEAPPLE using the send_message tool." });
    }
    const text = await tool("wait_for_messages", { timeout_seconds: 5 });
    if (text.startsWith("No messages")) continue;
    log.push(`[${((Date.now() - t0) / 1000).toFixed(1)}s] ${text}`);
    if (!seen.pineapple && /PINEAPPLE/i.test(text)) {
      seen.pineapple = true;
      await tool("send_message", { to: name, body: "Thanks. One more: reply to me (codex) with the single word MANGO using send_message." });
    }
    if (!seen.mango && /MANGO/i.test(text)) {
      seen.mango = true;
      await new Promise((r) => setTimeout(r, 3000));
      await tool("send_message", { to: name, body: "And the last one: reply to me (codex) with the single word LIME using send_message." });
    }
    if (/LIME/i.test(text)) seen.lime = true;
  }
})();

const settings = { permissions: { allow: ["mcp(ainterlinked/*)"] } };
const agy = spawn(AGY, [
  "-p", "Two edits in this folder: replace the content of held.txt with the word CHANGED, and create free.txt with the word OK. Use the file editing tools and the ainterlinked tools only, no terminal commands: they aren't allowed in this run. If an edit is refused, don't retry it or work around it. You are also connected to ainterlinked, a bus shared with other AI agents: answer what your peer agents ask for in their messages, then finish.",
  "--output-format", "stream-json", "--print-timeout", "240s", "--mode", "accept-edits", "--settings", JSON.stringify(settings),
  ...(process.env.AGY_MODEL ? ["--model", process.env.AGY_MODEL] : []),
], { windowsHide: true, env, cwd: PROJECT, stdio: ["ignore", "pipe", "pipe"] });
let out = "", err = "";
agy.stdout.on("data", (d) => (out += d));
agy.stderr.on("data", (d) => (err += d));
const limit = setTimeout(() => { console.log("time limit: ending agy"); agy.kill(); }, LIMIT_MS);
const code = await new Promise((r) => { agy.on("exit", r); agy.on("error", (e) => { err += e; r(-1); }); });
clearTimeout(limit);
await new Promise((r) => setTimeout(r, 1500));
const agents = await tool("list_agents", { include_offline: true });
const feed = await tool("activity_feed", { limit: 60 });
const holds = join(HOME, "holds");
const turnsHeld = existsSync(holds) ? readdirSync(holds).map((f) => Number(readFileSync(join(holds, f), "utf8"))).reduce((a, b) => a + b, 0) : 0;
done = true;
await talk;

const held = readFileSync(join(PROJECT, "held.txt"), "utf8");
const free = existsSync(join(PROJECT, "free.txt")) ? readFileSync(join(PROJECT, "free.txt"), "utf8") : "";
console.log(`agy exited ${code} after ${((Date.now() - t0) / 1000).toFixed(1)}s`);
console.log("agy's last output:", out.trim().slice(-1500));
if (err.trim()) console.log("agy's stderr:", err.trim().slice(-1500));
console.log("agents:\n" + agents + "\nfeed:\n" + feed);
console.log("peer received:\n" + log.join("\n---\n") + "\n");
const checks = [
  ["its SessionStart hook put it on the bus, in this project", seen.joined],
  ["it read a peer's message and replied PINEAPPLE", seen.pineapple],
  ["the follow-up reached it and it replied MANGO", seen.mango],
  ["the Stop hook held its turn open for the last one and it replied LIME", seen.lime && turnsHeld > 0],
  ["its edit of a file a peer holds didn't go through", held === "original\n"],
  ["its edit of a free file did", /OK/.test(free)],
];
for (const [name, ok] of checks) console.log(`${ok ? "PASS" : "FAIL"}  ${name}`);
const ok = checks.every((c) => c[1]);
peer.stdin.end();
await new Promise((r) => peer.on("exit", r));
// The isolated hub would stay for half an hour, with its files open.
const hub = spawnSync(IL, ["hub", "status"], { windowsHide: true, env, encoding: "utf8" }).stdout?.match(/^hub process .*pid (\d+)/m)?.[1];
if (hub) { try { process.kill(Number(hub)); } catch {} await new Promise((r) => setTimeout(r, 1000)); }
if (ok) {
  rmSync(HOME, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  rmSync(PROJECT, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
} else {
  console.log("kept for debugging:", HOME, PROJECT);
  console.log("agy's log: ~/.gemini/antigravity-cli/cli.log");
}
process.exit(ok ? 0 : 1);
