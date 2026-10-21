import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { assessRun, measuredAllowance, proofPlan, proofSelection, retainJournals, runEnding, sourceHash, startupFailure, startupWaitMs, verifyRun } from "../tools/compaction-proof.mjs";
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

test("assembly accounting names a missing task and its event sequence 2w3", () => {
  const checked = assessRun("missing", [budget(1, 950), { seq: 2, type: "compaction" }, request(3, "continue"), response(4, 400), stopped(5)], task, "0001\n", 1, 1);
  assert.equal(checked.conditions[4].message, "turn 1 task missing from model.request seq 3 after compaction seq 2");
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
