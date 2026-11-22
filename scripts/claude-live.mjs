// Live test against a real Claude Code (uses your Claude plan's model usage:
// one short Haiku session, capped turns). Fully isolated: a temporary
// AINTERLINKED_HOME, --strict-mcp-config and --setting-sources project, so your
// own MCP servers, hooks and config files are not loaded or touched.
//
// A peer agent (a real `ainterlinked mcp --harness codex` process) asks Claude
// for PINEAPPLE; when that arrives it immediately asks for MANGO, which can
// only reach Claude through the Stop hook holding the session open.
//
//   node scripts/claude-live.mjs [path/to/ainterlinked]
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createInterface } from "node:readline";

const BIN = resolve(process.argv[2] ?? (process.platform === "win32" ? "bin/ainterlinked.exe" : "bin/ainterlinked"));
const HOME = mkdtempSync(join(tmpdir(), "il-live-"));
const PROJECT = mkdtempSync(join(tmpdir(), "demo-"));
const env = { ...process.env, AINTERLINKED_HOME: HOME };
const cmd = BIN.replaceAll("\\", "/");

const mcpConfig = join(HOME, "mcp.json");
writeFileSync(mcpConfig, JSON.stringify({ mcpServers: { ainterlinked: { type: "stdio", command: BIN, args: ["mcp", "--harness", "claude-code"] } } }));
const hook = (ev, matcher) => ({ ...(matcher ? { matcher } : {}), hooks: [{ type: "command", command: `${cmd} hook claude-code ${ev}`, timeout: 15 }] });
const settings = join(HOME, "settings.json");
writeFileSync(settings, JSON.stringify({
  permissions: { allow: ["mcp__ainterlinked"] },
  hooks: {
    SessionStart: [hook("SessionStart")], UserPromptSubmit: [hook("UserPromptSubmit")],
    PreToolUse: [hook("PreToolUse", "Edit|Write")], PostToolUse: [hook("PostToolUse")],
    Stop: [hook("Stop")], SessionEnd: [hook("SessionEnd")],
  },
}));

// Peer agent.
const peer = spawn(BIN, ["mcp", "--harness", "codex"], { env, cwd: PROJECT, stdio: ["pipe", "pipe", "inherit"] });
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
await tool("set_status", { state: "working", activity: "waiting for the Claude agent" });

const log = [];
const results = { pineapple: false, mango: false };
const watcher = (async () => {
  for (let round = 0; round < 2; round++) {
    const deadline = Date.now() + 150_000;
    while (Date.now() < deadline) {
      const text = await tool("wait_for_messages", { timeout_seconds: 30 });
      if (text.startsWith("No messages")) continue;
      log.push(text);
      if (round === 0 && /PINEAPPLE/i.test(text)) {
        results.pineapple = true;
        await tool("send_message", { to: "project", body: "Thanks. One more: reply to me (codex) with the single word MANGO using send_message." });
        break;
      }
      if (round === 1 && /MANGO/i.test(text)) { results.mango = true; return; }
    }
  }
})();

// Claude joins after the peer asked for something.
await new Promise((r) => setTimeout(r, 300));
// The peer can't address Claude by name before it exists, so it posts to the
// project; Claude gets it through the SessionStart briefing.
const asker = setInterval(async () => {
  const list = await tool("list_agents");
  const m = list.match(/(claude@\S+)/);
  if (m) {
    clearInterval(asker);
    await tool("send_message", { to: m[1], body: "Hi, I'm the codex agent. Please reply to me with the single word PINEAPPLE using the send_message tool." });
  }
}, 300);

const t0 = Date.now();
const claudeBin = process.platform === "win32"
  ? execFileSync("where.exe", ["claude"], { encoding: "utf8" }).split(/\r?\n/).find((l) => l.toLowerCase().endsWith(".exe"))
  : "claude";
const claude = spawn(claudeBin, [
  "-p", "You are connected to ainterlinked, a bus shared with other AI agents. Do what your peer agents ask you in their messages, then finish.",
  "--model", "haiku", "--max-turns", "12",
  "--setting-sources", "project", "--settings", settings,
  "--strict-mcp-config", "--mcp-config", mcpConfig,
  "--allowedTools", "mcp__ainterlinked",
], { env, cwd: PROJECT, stdio: ["ignore", "pipe", "pipe"] });
let out = "";
claude.stdout.on("data", (d) => (out += d));
claude.stderr.on("data", (d) => (out += d));
const code = await new Promise((r) => claude.on("exit", r));
clearInterval(asker);
await Promise.race([watcher, new Promise((r) => setTimeout(r, 5000))]);

console.log(`claude exited ${code} after ${((Date.now() - t0) / 1000).toFixed(1)}s`);
console.log("claude said:", out.trim().slice(0, 400));
console.log("peer received:\n" + log.join("\n---\n"));
console.log(`\n${results.pineapple ? "PASS" : "FAIL"}  Claude read the peer message (SessionStart/tool injection) and replied PINEAPPLE`);
console.log(`${results.mango ? "PASS" : "FAIL"}  Stop hook held Claude for the follow-up and it replied MANGO`);
peer.stdin.end();
await new Promise((r) => peer.on("exit", r));
if (!(results.pineapple && results.mango)) {
  console.log("kept for debugging:", HOME);
} else {
  rmSync(HOME, { recursive: true, force: true });
  rmSync(PROJECT, { recursive: true, force: true });
}
process.exit(results.pineapple && results.mango ? 0 : 1);
