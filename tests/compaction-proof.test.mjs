import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { assessRun, drivenStepPlan, measuredAllowance, proofPlan, proofSelection, retainJournals, runEnding, sourceHash, startupFailure, startupWaitMs, uniqueEvents, verifyRun } from "../tools/compaction-proof.mjs";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const task = "Read every numbered text file in this folder in filename order. After each read, append that file's first line to notes.md. Finish by saying how many numbered files there were.";
const budget = (seq, usedEst, usedMeasured = usedEst) => ({ seq, type: "budget", data: { used_est: usedEst, used_measured: usedMeasured, ceiling: 1000, categories: { system: 100 } } });
const response = (seq, promptTokens) => ({ seq, type: "model.response", data: { usage: { prompt_tokens: promptTokens } } });
const request = (seq, content = task) => ({ seq, type: "model.request", body: { messages: [{ role: "user", content }] } });
const stopped = (seq) => ({ seq, type: "run.stopped", data: { reason: "done" } });
const good = [budget(1, 950), { seq: 2, type: "compaction" }, request(3), response(4, 400), budget(5, 400), stopped(6)];

test("the proof accepts a more-than-half real-count drop with the task retained", () => {
  assert.equal(verifyRun("fixture", good, task, "0001\n", 1, 1).compactions.length, 1);
});

test("assembly accounting names a crossing with no compaction 2w3", () => {
  const checked = assessRun("crossing", [budget(1, 950), request(2), response(3, 950), stopped(4)], task, "0001\n", 1, 1);
  assert.deepEqual(checked.rows.map(({ turn, prompt, line, crossed, ran }) => ({ turn, prompt, line, crossed, compactions: ran.length })), [
    { turn: 1, prompt: 950, line: 910, crossed: true, compactions: 0 }
  ]);
  assert.equal(checked.conditions[0].message, "turn 1 crossed line 910 with prompt 950 but saw 0 compactions");
});

test("assembly accounting names a compaction below the line 2w3", () => {
  const checked = assessRun("below", [budget(1, 800), { seq: 2, type: "compaction" }, request(3), response(4, 300), stopped(5)], task, "0001\n", 1, 1);
  assert.equal(checked.conditions[0].message, "turn 1 compacted below line 910 with prompt 800");
});

test("assembly accounting names a zero-percent cut 2w3", () => {
  const checked = assessRun("zero", [budget(1, 950), { seq: 2, type: "compaction" }, request(3), response(4, 950), stopped(5)], task, "0001\n", 1, 1);
  assert.equal(checked.conditions[1].message, "turn 1 compaction changed changeable prompt by 0.0% (850 to 850)");
});

test("assembly accounting reads the response after the post-compaction budget 2w4", () => {
  const checked = assessRun("delayed", [budget(1, 950), { seq: 2, type: "compaction" }, budget(3, 400), request(4), response(5, 400), stopped(6)], task, "0001\n", 1, 1);
  assert.equal(checked.conditions[1].pass, true);
  assert.equal(checked.result.compactions[0].after, 400);
});

test("assembly accounting names a missing task and its event sequence 2w3", () => {
  const checked = assessRun("missing", [budget(1, 950), { seq: 2, type: "compaction" }, request(3, "continue"), response(4, 400), stopped(5)], task, "0001\n", 1, 1);
  assert.equal(checked.conditions[4].message, "task absent after compaction seq 2; ids=");
});

test("a driven case crosses twice and compacts twice 2w4", () => {
  const answered = (seq) => ({ ...response(seq, 400), data: { ...response(seq, 400).data, content: "answered" } });
  const twice = [budget(1, 950), { seq: 2, type: "compaction" }, request(3), answered(4), budget(5, 400), budget(6, 950), { seq: 7, type: "compaction" }, request(8), answered(9), budget(10, 400), stopped(11)];
  assert.equal(verifyRun("fresh", twice, task, "", 0, 2, true).compactions.length, 2);
  assert.deepEqual(drivenStepPlan(24576, 2666, 3), { tokens: 2050, firstCrossing: 10, maximum: 30 });
});

test("a driven empty post-compaction reply is attributed to the model 2w5", () => {
  const checked = assessRun("fresh", good, task, "", 0, 1, false, "", true);
  assert.equal(checked.conditions[3].pass, true);
  assert.equal(checked.replies[0].reading, "MODEL");
});

test("a driven missing task still names the request sequence 2w4", () => {
  const events = good.map((one) => one.type === "model.request" ? request(one.seq, "filler only") : one.type === "model.response" ? { ...one, data: { ...one.data, content: "answered" } } : one);
  assert.equal(assessRun("fresh", events, task, "", 0, 1, false, "", true).conditions[4].message, "task absent after compaction seq 2; ids=");
});

test("session messages decide task retention after compaction 2w5", () => {
  const compact = { seq: 2, type: "compaction", data: { affected_ids: ["old"], summary_message_id: "handoff" } };
  const events = [budget(1, 950), compact, response(4, 400), budget(5, 400), stopped(6)];
  const handoff = { id: "handoff", role: "user", content: task, tokens: 40 };
  const kept = { id: "kept", role: "assistant", content: "working" };
  const present = assessRun("fresh", events, task, "", 0, 1, false, "", true, new Map([[2, [handoff, kept]]]));
  assert.equal(present.conditions[4].pass, true);
  assert.deepEqual(present.taskLocations[0], { compact: 2, role: "user", id: "handoff", position: 1, affected: false, ids: ["handoff", "kept"] });
  const absent = assessRun("fresh", events, task, "", 0, 1, false, "", true, new Map([[2, [kept]]]));
  assert.equal(absent.conditions[4].pass, false);
  assert.match(absent.conditions[4].message, /absent.*kept/);
});

test("post-compaction empty replies distinguish model from malformed request 2w5", () => {
  const compact = { seq: 2, type: "compaction", data: { summary_message_id: "handoff" } };
  const reasoning = { ...response(4, 400), data: { ...response(4, 400).data, content: "", finish_reason: "stop", reasoning_tokens: 7, usage: { prompt_tokens: 400, completion_tokens: 9 } } };
  const events = [budget(1, 950), compact, reasoning, budget(5, 400), stopped(6)];
  const model = assessRun("fresh", events, task, "", 0, 1, false, "", true, new Map([[2, [{ id: "handoff", role: "user", content: task, tokens: 40 }]]]));
  assert.equal(model.replies[0].reading, "MODEL");
  assert.equal(model.conditions[3].pass, true);
  const malformed = assessRun("fresh", events, task, "", 0, 1, false, "", true, new Map([[2, [{ id: "orphan", role: "tool", content: "result", tool_call_id: "missing" }]]]));
  assert.equal(malformed.replies[0].reading, "HARNESS");
  assert.equal(malformed.conditions[3].pass, false);
});

test("the two negative controls fail on no compaction and missing notes", () => {
  assert.throws(() => verifyRun("off", good.filter((one) => one.type !== "compaction"), task, "0001\n", 1, 1), /crossed line 910.*0 compactions/);
  assert.throws(() => verifyRun("empty hand-off", good, task, "", 1, 1), /notes\.md/);
  assert.throws(() => verifyRun("unreachable", good.map((one) => one.type === "run.stopped" ? { ...one, data: { reason: "model_unreachable" } } : one), task, "0001\n", 1, 1), /run ended model_unreachable/);
});

test("scored-run journals are retained with connection redactions 2w3", async () => {
  const root = await mkdtemp(join(tmpdir(), "agentb-compaction-journal-")), evidence = join(root, "evidence");
  try {
    await mkdir(join(root, "data", "logs"), { recursive: true });
    await writeFile(join(root, "data", "logs", "run.jsonl"), '{"address":"private-host"}\n');
    await writeFile(join(root, "data", "logs", "ignored.txt"), "private-host\n");
    await retainJournals(join(root, "data"), evidence, "fresh", ["private-host"]);
    assert.equal(await readFile(join(evidence, "fresh", "logs", "run.jsonl"), "utf8"), '{"address":"<connection>"}\n');
  } finally { removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction journal test cleanup"); }
});

test("the same event retained in two journals is one proof event 2w3", () => {
  const event = { session_id: "proof", run_id: "run", seq: 7, type: "compaction" };
  assert.deepEqual(uniqueEvents([event, structuredClone(event)]), [event]);
});

test("the proof uses the measured start allowance and the active agent model 2ur", () => {
  assert.equal(startupWaitMs, 8036);
  const selected = proofSelection({ connections: [{ id: "main", model: "chosen", models: [{ model: "chosen", context: { n_ctx: 200000 } }] }] }, { agents: [{ name: "agent_b", b: "main", c: "other" }] });
  assert.deepEqual(selected.record, { connection: "active profile agent_b role b", model: "chosen", window: 200000 });
  assert.equal(selected.agent.c, "");
  assert.equal(startupFailure(startupWaitMs, "agents[0].c: invalid"), "candidate page did not answer after 8.036 seconds; last log: agents[0].c: invalid");
  assert.equal(measuredAllowance(1000, [500, 5000]), 60000);
  assert.equal(measuredAllowance(1000, [500, 5000], 30), 360000);
});

test("every proof run uses the installed model window and one scaled workload 2vr", () => {
  const machine = { connections: [{ id: "main", model: "chosen", models: [{ model: "chosen", context: { n_ctx: 32768 } }] }] };
  const profile = { agents: [{ name: "agent_b", b: "main" }] };
  const plan = proofPlan(machine, profile);
  assert.equal(plan.window, 32768);
  assert.ok(plan.runs.every((run) => run.window <= plan.window));
  assert.deepEqual(plan.runs.map((run) => run.files), [120, 120, 120, 120, 40]);
});

test("a stalled or turnless trace names its ending 2vr", () => {
  const repeated = [budget(1, 500), budget(2, 500)];
  assert.match(runEnding(repeated, new Error("fresh completion timed out after measured allowance")), /^expired allowance:/);
  assert.equal(runEnding([{ seq: 1, type: "run.stopped", data: { reason: "model_unreachable" } }]), "terminal event: run.stopped model_unreachable");
});

test("the tracked source hash excludes tests and is stable", async () => {
  assert.match(await sourceHash(), /^[0-9a-f]{64}$/);
});

test("the release gate skips an equal hash and refuses an unreachable changed source", async () => {
  const root = await mkdtemp(join(tmpdir(), "agentb-compaction-gate-"));
  try {
    await mkdir(join(root, "internal", "context"), { recursive: true }); await mkdir(join(root, "tests"));
    const source = join(root, "internal", "context", "one.go"), record = join(root, "tests", "compaction-proof.json");
    await writeFile(source, "package contextmgr\n"); await writeFile(record, JSON.stringify({ source_hash: await sourceHash(root) }));
    const equal = spawnSync(process.execPath, [resolve("tools/compaction-proof.mjs"), "--mode", "gate", "--source", root, "--record", record], { encoding: "utf8", windowsHide: true });
    assert.equal(equal.status, 0); assert.match(equal.stdout, /source unchanged/);
    await writeFile(source, "package contextmgr\n ");
    const raw = join(root, "evidence", "gate.txt");
    const changed = spawnSync(process.execPath, [resolve("tools/compaction-proof.mjs"), "--mode", "gate", "--source", root, "--record", record, "--raw-output", raw], { encoding: "utf8", windowsHide: true });
    assert.equal(changed.status, 1); assert.match(changed.stderr, /^BUILT, NOT RELEASED: compaction is not proved: model unreachable/m);
    assert.equal(await readFile(raw, "utf8"), changed.stderr);
  } finally { removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction gate test cleanup"); }
});
