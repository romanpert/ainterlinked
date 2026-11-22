// End-to-end smoke test with real processes. Node plays two harnesses: each
// spawns `interlinked mcp` (like a harness starts its MCP server) and runs
// hooks through a shell (like Claude Code does), so the real process-tree
// anchoring, the SQLite hub shared between processes, the CLI and the HTTP
// server are all exercised. No model or paid API is involved.
//
//   node scripts/e2e.mjs [path/to/interlinked]
import { spawn, spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { createInterface } from "node:readline";

const BIN = resolve(process.argv[2] ?? (process.platform === "win32" ? "bin/interlinked.exe" : "bin/interlinked"));
const HOME = mkdtempSync(join(tmpdir(), "il-e2e-"));
const PROJECT = mkdtempSync(join(tmpdir(), "il-proj-"));
const env = { ...process.env, INTERLINKED_HOME: HOME };
delete env.INTERLINKED_ANCHOR_PID;
let failures = 0;
const ok = (cond, what, extra = "") => {
  console.log(`${cond ? "PASS" : "FAIL"}  ${what}${extra ? "  — " + extra : ""}`);
  if (!cond) failures++;
};

class MCP {
  constructor(harness) {
    this.p = spawn(BIN, ["mcp", "--harness", harness], { env, cwd: PROJECT, stdio: ["pipe", "pipe", "inherit"] });
    this.id = 0;
    this.waiting = new Map();
    createInterface({ input: this.p.stdout }).on("line", (line) => {
      const m = JSON.parse(line);
      if (m.method === "roots/list") return this.send({ jsonrpc: "2.0", id: m.id, result: { roots: [] } });
      const w = this.waiting.get(String(m.id));
      if (w) { this.waiting.delete(String(m.id)); w(m); }
    });
  }
  send(m) { this.p.stdin.write(JSON.stringify(m) + "\n"); }
  call(method, params) {
    const id = ++this.id;
    return new Promise((res) => { this.waiting.set(String(id), res); this.send({ jsonrpc: "2.0", id, method, params }); });
  }
  async init() {
    const r = await this.call("initialize", { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "e2e", version: "1" } });
    this.send({ jsonrpc: "2.0", method: "notifications/initialized" });
    return r.result;
  }
  async tool(name, args = {}) {
    const r = await this.call("tools/call", { name, arguments: args });
    return { text: r.result.content[0].text, isError: r.result.isError };
  }
  close() { this.p.stdin.end(); return new Promise((r) => this.p.on("exit", r)); }
}

// Hooks run through a shell, as harnesses do.
function hook(harness, event, payload) {
  const shell = process.platform === "win32" ? "bash" : "sh";
  const cmd = `"${BIN.replaceAll("\\", "/")}" hook ${harness} ${event}`;
  const t0 = Date.now();
  const r = spawnSync(shell, ["-c", cmd], { env, cwd: PROJECT, input: JSON.stringify({ cwd: PROJECT, ...payload }), encoding: "utf8" });
  return { out: r.stdout.trim() ? JSON.parse(r.stdout) : null, ms: Date.now() - t0, code: r.status };
}
// The human operator types in a terminal of their own, outside every agent's
// harness, and the hub process checks the caller's whole chain of parents.
// This test may itself run inside an agent (a developer's Claude Code), so
// "the human" here is the CLI started from a process whose parent has exited:
// detached from every harness. (The same detaching is open to an agent on the
// same OS account: the real boundary for that is running the hub as another
// account.)
const ORPHAN = `
const cp=require('child_process'),fs=require('fs');
const [bin,out,...args]=process.argv.slice(1);
if(process.env.IL_STAGE!=='2'){
  cp.spawn(process.execPath,['-e',process.env.IL_ORPHAN,...process.argv.slice(1)],{detached:true,stdio:'ignore',env:{...process.env,IL_STAGE:'2'}}).unref();
  process.exit(0);
}
const ppid=process.ppid;
for(let i=0;i<200;i++){try{process.kill(ppid,0)}catch{break}Atomics.wait(new Int32Array(new SharedArrayBuffer(4)),0,0,25)}
const r=cp.spawnSync(bin,args,{encoding:'utf8'});
fs.writeFileSync(out+'.tmp',JSON.stringify({status:r.status,stdout:r.stdout||'',stderr:r.stderr||''}));
fs.renameSync(out+'.tmp',out);`;
let cliRuns = 0;
const sleepSync = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
const cli = (...args) => {
  const out = join(HOME, `cli-${++cliRuns}.json`);
  spawnSync(process.execPath, ["-e", ORPHAN, BIN, out, ...args], { env: { ...env, IL_ORPHAN: ORPHAN }, cwd: PROJECT });
  for (let i = 0; i < 600 && !existsSync(out); i++) sleepSync(25);
  return existsSync(out) ? JSON.parse(readFileSync(out, "utf8")) : { status: -1, stdout: "", stderr: "the detached CLI never answered" };
};
// The same CLI run straight from the "harness" (an agent's shell), and
// wrapped in another program (node) by it: both are that agent.
const agentCli = (...args) => spawnSync(BIN, args, { env, cwd: PROJECT, encoding: "utf8" });
const WRAP = "const c=require('child_process').spawn(process.argv[1],process.argv.slice(2),{stdio:'inherit'});c.on('exit',(x)=>process.exit(x??1))";
const wrappedCli = (...args) => spawnSync(process.execPath, ["-e", WRAP, BIN, ...args], { env, cwd: PROJECT, encoding: "utf8" });
const killTree = (p) => (process.platform === "win32" ? spawnSync("taskkill", ["/T", "/F", "/PID", String(p.pid)]) : p.kill());

try {
  const a = new MCP("claude-code");
  const init = await a.init();
  ok(init.serverInfo.name === "interlinked", "MCP initialize");
  const who = await a.tool("whoami");
  ok(/You are claude@il-proj-/.test(who.text), "agent A registered from the real cwd", who.text.split("\n")[0]);

  // The SessionStart hook of the same "harness process" must join agent A, not create a new one.
  const h = hook("claude-code", "SessionStart", { session_id: "sess-A" });
  ok(h.code === 0 && h.out?.hookSpecificOutput?.additionalContext?.includes("You are \"claude@il-proj"), "hook links to the same agent via process anchor", `${h.ms} ms`);
  const list1 = cli("agents").stdout;
  ok((list1.match(/claude@/g) || []).length === 1, "one agent, not two (MCP + hook unified)");
  ok(/delivery turn/.test(list1), "delivery upgraded to turn once hooks report in");

  const b = new MCP("codex");
  await b.init();
  await b.tool("set_status", { state: "working", activity: "migrating the users table" });
  const list2 = (await a.tool("list_agents")).text;
  ok(list2.includes("codex@il-proj") && list2.includes("migrating the users table"), "A sees B and what it is doing");

  // B waits, A sends, B's wait returns.
  const waited = b.tool("wait_for_messages", { timeout_seconds: 20 });
  await new Promise((r) => setTimeout(r, 400));
  const t0 = Date.now();
  const sent = await a.tool("send_message", { to: "project", body: "schema is ready, start the API" });
  ok(!sent.isError, "send to project", sent.text);
  const got = await waited;
  ok(got.text.includes("schema is ready"), "wait_for_messages wakes up on delivery", `${Date.now() - t0} ms`);

  // Human operator via CLI -> agent; agent -> human.
  ok(cli("send", "project:" + PROJECT.split(/[\\/]/).pop(), "please", "rebase", "on", "main").status === 0, "human sends from the CLI");
  const injected = hook("claude-code", "PostToolUse", { session_id: "sess-A", tool_name: "Bash", tool_input: { command: "ls" } });
  ok(injected.out?.hookSpecificOutput?.additionalContext?.includes("please rebase on main"), "human message injected after a tool call", `${injected.ms} ms`);
  await a.tool("send_message", { to: "human", body: "done rebasing" });
  ok(cli("inbox").stdout.includes("done rebasing"), "agent reaches the human inbox");
  // Identity comes from the kernel: the CLI inside an agent's harness is that
  // agent, and can't read or act as the human.
  if (env.INTERLINKED_HUB_PROCESS !== "0") {
    const status = cli("hub", "status").stdout;
    ok(status.includes("the human operator"), "hub process knows the terminal is the human", status.split("\n")[1] || status);
    const asAgent = agentCli("inbox");
    ok(asAgent.status !== 0 && (asAgent.stderr + asAgent.stdout).includes("inside an agent's harness"), "the CLI in an agent's shell can't act as the human", (asAgent.stderr || asAgent.stdout).trim().split("\n")[0]);
    const wrapped = wrappedCli("inbox");
    ok(wrapped.status !== 0 && (wrapped.stderr + wrapped.stdout).includes("inside an agent's harness"), "nor the CLI wrapped in another program by the agent", (wrapped.stderr || wrapped.stdout).trim().split("\n")[0]);
  }

  // File reservation enforced through the PreToolUse hook.
  await b.tool("reserve_files", { paths: ["db/"], reason: "migration" });
  const denied = hook("claude-code", "PreToolUse", { session_id: "sess-A", tool_name: "Edit", tool_input: { file_path: join(PROJECT, "db", "schema.sql") } });
  ok(denied.out?.hookSpecificOutput?.permissionDecision === "deny", "edit of a reserved file is blocked", denied.out?.hookSpecificOutput?.permissionDecisionReason?.slice(0, 70));

  // Stop is held for mail meant for this agent, not for group announcements.
  const nameA = who.text.match(/You are (\S+)/)[1];
  await b.tool("send_message", { to: "project", body: "FYI: pushed dev" });
  const free = hook("claude-code", "Stop", { session_id: "sess-A" });
  ok(free.out?.decision !== "block", "Stop not held for a project brief");
  await b.tool("send_message", { to: nameA, body: "one more thing" });
  const stop = hook("claude-code", "Stop", { session_id: "sess-A" });
  ok(stop.out?.decision === "block" && stop.out.reason.includes("one more thing"), "Stop held with a direct message");

  // HTTP dashboard state.
  const port = 17700 + Math.floor(Math.random() * 200);
  const srv = spawn(process.execPath, ["-e", WRAP, BIN, "serve", "--listen", `127.0.0.1:${port}`], { env, stdio: "ignore" });
  let state = null;
  for (let i = 0; i < 30 && !state; i++) {
    await new Promise((r) => setTimeout(r, 150));
    try { state = await (await fetch(`http://127.0.0.1:${port}/api/state`)).json(); } catch {}
  }
  ok(state && state.agents.length >= 2 && state.messages.length >= 3, "dashboard /api/state", state ? `${state.agents.length} agents, ${state.messages.length} messages` : "no response");
  const foreign = await fetch(`http://127.0.0.1:${port}/api/state`, { headers: { Origin: "http://evil.example" } });
  ok(foreign.status === 403, "cross-origin request refused");
  killTree(srv);

  // Hook latency budget, relative to what a bare shell spawn costs on this
  // machine right now (antivirus and load can make any spawn slow).
  const median = (f) => { const t = []; for (let i = 0; i < 5; i++) t.push(f()); t.sort((x, y) => x - y); return t[2]; };
  const shell = process.platform === "win32" ? "bash" : "sh";
  const base = median(() => { const t0 = Date.now(); spawnSync(shell, ["-c", "true"]); return Date.now() - t0; });
  const hookMs = median(() => hook("claude-code", "PostToolUse", { session_id: "sess-A", tool_name: "Read" }).ms);
  // Informational: a freshly built, unsigned binary can pay antivirus scans
  // on every spawn. interlinked's own work is guarded in-process by
  // internal/hooks/timing_test.go; here we only fail on a real hang.
  // The ceiling scales with how slow spawning is right now: on a box running
  // dozens of agent sessions a bare shell alone can take ~1 s.
  const ceiling = Math.max(2000, base * 6);
  ok(hookMs < ceiling, "hook median latency (fails only on a hang)", `${hookMs} ms (bare shell spawn here: ${base} ms, ceiling ${ceiling} ms)`);

  await a.close();
  await new Promise((r) => setTimeout(r, 300));
  const left = (await b.tool("list_agents")).text;
  // A closed MCP server may be a restart: the agent is "leaving" and goes
  // offline only if it doesn't check in again within the grace (30 s).
  ok(/claude@\S+ \[claude-code\] leaving/.test(left), "closing the MCP server marks the agent leaving", left.split("\n").find((l) => l.includes("claude@")) || "");
  await b.close();
} catch (e) {
  failures++;
  console.error("FAIL  crashed:", e);
} finally {
  // Stop this run's hub process (it would idle out), then clean up.
  const hello = cli("hub", "status").stdout.match(/pid (\d+)/);
  if (hello) { try { process.kill(Number(hello[1])); } catch {} }
  await new Promise((r) => setTimeout(r, 400));
  rmSync(HOME, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  rmSync(PROJECT, { recursive: true, force: true });
}
console.log(failures ? `\n${failures} failure(s)` : "\nall checks passed");
process.exit(failures ? 1 : 0);
