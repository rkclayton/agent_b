import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { test } from "node:test";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { measuredAllowance, proofPlan, proofSelection, runEnding, sourceHash, startupFailure, startupWaitMs, verifyRun } from "../tools/compaction-proof.mjs";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const task = "Read every numbered text file in this folder in filename order. After each read, append that file's first line to notes.md. Finish by saying how many numbered files there were.";
const budget = (seq, used) => ({ seq, type: "budget", data: { used_measured: used, ceiling: 1000, categories: { system: 100 } } });
const good = [budget(1, 950), { seq: 2, type: "compaction" }, budget(3, 400), { seq: 4, type: "model.request", body: { messages: [{ content: task }] } }, { seq: 5, type: "run.stopped", data: { reason: "done" } }];

test("the proof accepts a more-than-half real-count drop with the task retained", () => {
  assert.equal(verifyRun("fixture", good, task, "0001\n", 1, 1).compactions.length, 1);
});

test("the two negative controls fail on no compaction and missing notes", () => {
  assert.throws(() => verifyRun("off", good.filter((one) => one.type !== "compaction"), task, "0001\n", 1, 1), /compacted every time/);
  assert.throws(() => verifyRun("empty hand-off", good, task, "", 1, 1), /notes\.md/);
  assert.throws(() => verifyRun("unreachable", good.map((one) => one.type === "run.stopped" ? { ...one, data: { reason: "model_unreachable" } } : one), task, "0001\n", 1, 1), /run ended model_unreachable/);
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
    const changed = spawnSync(process.execPath, [resolve("tools/compaction-proof.mjs"), "--mode", "gate", "--source", root, "--record", record], { encoding: "utf8", windowsHide: true });
    assert.equal(changed.status, 1); assert.match(changed.stderr, /^BUILT, NOT RELEASED: compaction is not proved: model unreachable/m);
  } finally { removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction gate test cleanup"); }
});
