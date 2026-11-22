// Live check with the real Codex CLI: ONE bounded `codex exec` run on the
// real bus (spends subscription quota, so it is never part of CI).
//
//   node scripts/codex-live.mjs <path/to/codex.exe> [ainterlinked]
//
// 1. Codex calls whoami and waits for mail (MCP path).
// 2. The operator sends a code word; Codex must reply with it.
// 3. A second word is sent right after the reply; the Stop hook must hold
//    Codex's turn so it answers that one too (hook path).
// Codex only runs hooks the user has approved (/hooks in Codex). Until then
// step 3 is reported NOT RUN; this script never bypasses that approval.
import { spawn, spawnSync } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { basename, join } from "node:path";

const CODEX = process.argv[2];
const IL = process.argv[3] ?? "ainterlinked";
if (!CODEX) {
  console.error("usage: node scripts/codex-live.mjs <codex> [ainterlinked]");
  process.exit(2);
}
const HARD_LIMIT_MS = 6 * 60 * 1000;
const deadline = Date.now() + HARD_LIMIT_MS;
const DIR = mkdtempSync(join(tmpdir(), "il-codex-live-"));
const project = basename(DIR).toLowerCase();
const code = () => randomBytes(3).toString("hex").toUpperCase();
const w1 = "PAPAYA" + code();
const w2 = "MANGO" + code();
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const il = (...a) => spawnSync(IL, a, { encoding: "utf8" }).stdout ?? "";
let failures = 0;
const ok = (cond, what, extra = "") => {
  console.log(`${cond ? "PASS" : "FAIL"}  ${what}${extra ? "  — " + extra : ""}`);
  if (!cond) failures++;
};

const prompt = [
  "You are testing a message bus between AI agents. Do exactly this and nothing else:",
  "1. Call the ainterlinked tool whoami.",
  "2. If a peer message has already been shown to you, skip this step; otherwise call the ainterlinked tool wait_for_messages with timeout_seconds 120.",
  "3. When a message arrives, call the ainterlinked tool send_message with reply_to set to that message's id and a body that contains the code word the message asks for.",
  "If you are later shown more messages, answer each the same way.",
  "Do not run shell commands and do not edit files.",
].join("\n");

// CODEX_MODEL overrides the configured model for this run only.
const model = process.env.CODEX_MODEL ? ["-m", process.env.CODEX_MODEL] : [];
const cx = spawn(CODEX, ["exec", "--skip-git-repo-check", "--ephemeral", ...model,
  "-s", "read-only", "-C", DIR, prompt], { stdio: ["ignore", "pipe", "pipe"] });
let log = "";
cx.stdout.on("data", (d) => (log += d));
cx.stderr.on("data", (d) => (log += d));
const exited = new Promise((r) => cx.on("exit", r));
const killer = setTimeout(() => { console.log("hard limit reached, stopping codex"); cx.kill(); }, HARD_LIMIT_MS);

const waitFor = async (fn, ms = 2000) => {
  while (Date.now() < deadline) {
    const v = fn();
    if (v) return v;
    if (cx.exitCode !== null) return fn();
    await sleep(ms);
  }
  return null;
};

try {
  const line = await waitFor(() => il("agents").split("\n").find((l) => l.includes("[codex]") && l.toLowerCase().includes(project)));
  ok(Boolean(line), "codex joined the bus from its own MCP server", line?.trim().slice(0, 100));
  if (line) {
    const name = line.trim().split(/\s+/)[0];
    il("send", name, `Reply with the code word ${w1}.`);
    const t0 = Date.now();
    const got1 = await waitFor(() => il("inbox", "--all", "--peek").includes(w1), 1000);
    // Delivery itself takes ~1 s (hook injection or wait_for_messages); this
    // measures send → reply, which includes the model's own turn.
    ok(Boolean(got1), "codex received the message and replied", `${Math.round((Date.now() - t0) / 1000)} s send→reply`);
    const after = il("agents", "--all").split("\n").find((l) => l.includes(name + " "));
    const hooksRan = Boolean(after && /delivery turn/.test(after));
    if (!hooksRan) {
      console.log("NOT RUN  Stop-hook hold: Codex didn't run the ainterlinked hooks (approve them once with /hooks in Codex)");
    } else if (got1) {
      il("send", name, `One more: reply with the code word ${w2}.`);
      const got2 = await waitFor(() => il("inbox", "--all", "--peek").includes(w2), 1000);
      ok(Boolean(got2), "Stop hook held the turn and codex answered the second message");
    }
  }
  await Promise.race([exited, sleep(30000)]);
} finally {
  clearTimeout(killer);
  if (cx.exitCode === null) cx.kill();
  rmSync(DIR, { recursive: true, force: true });
}
console.log("\n--- codex output (tail) ---\n" + log.split("\n").slice(-25).join("\n"));
console.log(failures ? `\n${failures} failure(s)` : "\nall checks passed");
process.exit(failures ? 1 : 0);
