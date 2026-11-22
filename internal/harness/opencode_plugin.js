// ainterlinked plugin for OpenCode — written by `ainterlinked install`.
// Remove with `ainterlinked uninstall opencode` (or delete this file).
// It reports presence/activity to the ainterlinked bus, appends peer messages
// to tool results, blocks edits to files other agents reserved, and wakes
// idle sessions when a message arrives (true push delivery).
const BIN = "__AINTERLINKED_BIN__";
const POLL_MS = 3000;

export const AInterlinked = async ({ client, directory }) => {
  const idle = new Set();

  const run = async (event, payload) => {
    try {
      const p = Bun.spawn([BIN, "hook", "opencode", event], {
        windowsHide: true, // no console window when OpenCode has none of its own
        stdin: "pipe", stdout: "pipe", stderr: "ignore", env: process.env,
      });
      p.stdin.write(JSON.stringify({ cwd: directory, ...payload }));
      p.stdin.end();
      const out = (await new Response(p.stdout).text()).trim();
      await p.exited;
      return out ? JSON.parse(out) : {};
    } catch {
      return {};
    }
  };

  // Sessions we just woke: their next chat.message is ours, not the user's,
  // so it must not reset ainterlinked's auto-continue budget.
  const woke = new Map();

  const wake = async (sessionID, text) => {
    if (!text) return;
    idle.delete(sessionID);
    woke.set(sessionID, Date.now());
    try {
      await client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: "text", text }] },
      });
    } catch {}
  };

  const timer = setInterval(async () => {
    for (const id of [...idle]) {
      const r = await run("idle", { session_id: id });
      if (r.prompt) await wake(id, r.prompt);
    }
  }, POLL_MS);
  timer.unref?.();

  return {
    event: async ({ event }) => {
      const props = event?.properties ?? {};
      const sid = props.sessionID ?? props.info?.id;
      if (!sid) return;
      switch (event.type) {
        case "session.created":
          await run("SessionStart", { session_id: sid });
          break;
        case "session.idle": {
          idle.add(sid);
          const r = await run("idle", { session_id: sid });
          if (r.prompt) await wake(sid, r.prompt);
          break;
        }
        case "session.status":
          if (props.status?.type && props.status.type !== "idle") idle.delete(sid);
          break;
        case "session.deleted":
          idle.delete(sid);
          await run("SessionEnd", { session_id: sid });
          break;
      }
    },
    "chat.message": async (input) => {
      idle.delete(input.sessionID);
      const t = woke.get(input.sessionID);
      const auto = t !== undefined && Date.now() - t < 30000;
      woke.delete(input.sessionID);
      await run("prompt", { session_id: input.sessionID, ainterlinked_auto: auto });
    },
    "tool.execute.before": async (input, output) => {
      const r = await run("PreToolUse", { session_id: input.sessionID, tool_name: input.tool, tool_input: output.args });
      if (r.deny) throw new Error(r.deny);
    },
    "tool.execute.after": async (input, output) => {
      const r = await run("PostToolUse", { session_id: input.sessionID, tool_name: input.tool, tool_input: input.args });
      if (r.context && typeof output.output === "string") output.output += "\n\n" + r.context;
    },
  };
};
