// Item 2gg (v1.1.2/W4): one retained chat that cannot be restored must not
// stop Agent_b from starting, and must not take the other chats with it.
//
//   node scripts/restore-resilience-scenario.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
//
// The audit found cmd/harness/main.go calling log.Fatal on the first restore
// failure, so a chat naming an agent the operator had renamed or removed took
// the whole application down. This stages exactly that: one good chat, one
// chat naming an agent that does not exist.
import assert from "node:assert/strict";
import { mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { start } from "./ui-harness.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const dataRoot = resolve(args.data);
const chats = join(dataRoot, "chats");
await mkdir(chats, { recursive: true });

function journal(sessionID, agentID) {
  const at = new Date(Date.UTC(2026, 8, 20)).toISOString();
  const lines = [
    { seq: 1, at, session_id: sessionID, run_id: "", type: "session.created", data: { session: { id: sessionID, label: sessionID, agent_id: agentID, server_id: "ui", agent_name: agentID, b_profile: "ui", role: "b", created_at: at, closed: false, name_pinned: false, workspace: "" } } },
    { seq: 2, at, session_id: sessionID, run_id: "r1", type: "message.appended", data: { message: { id: "m-1", role: "user", content: `a message in ${sessionID}` } } },
    { seq: 3, at, session_id: sessionID, run_id: "r1", type: "run.started", data: { run_id: "r1", user_message_id: "m-1" } },
    { seq: 4, at, session_id: sessionID, run_id: "r1", type: "model.response", data: { content: `an answer in ${sessionID}`, finish_reason: "stop", duration_ms: 1 } },
    { seq: 5, at, session_id: sessionID, run_id: "r1", type: "run.stopped", data: { run_id: "r1", reason: "done", turns: 1, queue_held: false } },
  ];
  return lines.map((line) => JSON.stringify(line)).join("\n") + "\n";
}

await writeFile(join(chats, "good-chat.jsonl"), journal("good-chat", "ui"));
await writeFile(join(chats, "orphaned-chat.jsonl"), journal("orphaned-chat", "an-agent-that-was-removed"));

const results = [];
let failures = 0;
function record(name, ok, detail) {
  results.push({ name, ok, detail });
  if (!ok) failures++;
  process.stdout.write(`${ok ? "PASS" : "FAIL"} ${name}${ok ? "" : ` — ${JSON.stringify(detail)}`}\n`);
}

let harness = null;
try {
  // Starting at all is the first assertion: before the fix this never became
  // ready, because the process had already called log.Fatal.
  harness = await start({ exe: args.exe, appRoot: args["app-root"], data: dataRoot, reachable: true, readyTimeout: 90000 });
  record("2gg/a-chat-that-cannot-be-restored-does-not-stop-the-start", true, { ready: true });
  const state = await harness.getState();
  record("2gg/the-other-retained-chats-still-come-back", !!state.sessions["good-chat"], { sessions: Object.keys(state.sessions) });
  record("2gg/the-unrestorable-chat-is-not-invented", !state.sessions["orphaned-chat"], { sessions: Object.keys(state.sessions) });
  // Its journal is the record and nothing may delete it.
  const remaining = await readdir(chats);
  record("2gg/its-journal-is-left-on-disk", remaining.includes("orphaned-chat.jsonl"), { remaining });
  record("2gg/the-loss-is-said-out-loud", /orphaned-chat/.test(harness.stderr()), { stderr: harness.stderr().slice(-400) });
} catch (error) {
  record("2gg/a-chat-that-cannot-be-restored-does-not-stop-the-start", false, { error: error.message });
} finally {
  await harness?.stop();
}

await writeFile(join(resolve(args.evidence), "w4-restore-resilience.json"), JSON.stringify({ schema: 1, failures, results }, null, 2));
process.stdout.write(`RESTORE RESILIENCE ${failures === 0 ? "PASS" : "FAIL"} ${results.length - failures} of ${results.length}\n`);
await rm(dataRoot, { recursive: true, force: true }).catch(() => {});
process.exitCode = failures === 0 ? 0 : 1;
