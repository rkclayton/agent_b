import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
const load = async name => JSON.parse(await readFile(new URL(`../docs/agents-fixtures/${name}.json`, import.meta.url)));
const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i, hash = /^[0-9a-f]{64}$/;
const checks = {
  agent: x => uuid.test(x.id) && ["baseline", "custom"].includes(x.kind) && x.revision > 0 && x.defaults,
  revision: x => uuid.test(x.agent_id) && x.revision > 0 && x.assigned_by === "pc" && hash.test(x.instructions_hash),
  baseline_registry: x => Array.isArray(x) && x.length > 0 && x.every(v => hash.test(v.instructions_hash) && /^v\d+\.\d+\.\d+$/.test(v.first_shipped_in)),
  change: x => uuid.test(x.change_id) && uuid.test(x.agent_id) && x.base_revision > 0 && hash.test(x.base_hash) && Object.keys(x.patch ?? {}).length > 0,
  chat_binding: x => uuid.test(x.agent_id) && ["created", "use_latest", "continue_with", "migrated"].includes(x.event) && (x.bound_hash === null || hash.test(x.bound_hash)),
  turn: x => x.turn > 0 && uuid.test(x.agent_id) && (x.hash === null || hash.test(x.hash)),
  memory_note: x => uuid.test(x.agent_id) && x.note && x.origin_device && x.seq > 0,
  tombstone: x => uuid.test(x.agent_id) && x.deleted_at && x.device && uuid.test(x.change_id)
};
test("agents-1 valid fixtures match every documented record shape", async () => { const f = await load("valid"); assert.deepEqual(Object.keys(f).sort(), Object.keys(checks).sort()); for (const [n,c] of Object.entries(checks)) assert.ok(c(f[n]), n); });
test("agents-1 invalid fixtures fail for every documented reason", async () => { const f = await load("invalid"); assert.deepEqual(Object.keys(f).sort(), Object.keys(checks).sort()); for (const [n,c] of Object.entries(checks)) { assert.match(f[n]._reason, /\S/); assert.equal(Boolean(c(f[n])), false, `${n}: ${f[n]._reason}`); } });
test("the shipped baseline is byte-identical to the approved model after its harness header", async () => {
  const model = await readFile(new URL("../plan/AGENTS-MODEL.md", import.meta.url), "utf8"), prompt = await readFile(new URL("../prompts/system.md", import.meta.url), "utf8");
  const approved = model.match(/## Baseline text[\s\S]*?```\r?\n([\s\S]*?)\r?\n```/)[1].replace(/\r\n/g, "\n") + "\n";
  const shipped = prompt.replace(/^<!-- Harness blocks: ([^>]+) -->\r?\n/, "").replace(/\r\n/g, "\n");
  assert.equal(shipped, approved);
  assert.deepEqual([...prompt.matchAll(/\{\{([a-z_]+)\}\}/g)].map(x => x[1]).slice(0, 10), ["date","os_context","folders","workspace","tools","network_boundary","media_capabilities","agent","project","memory"]);
});
