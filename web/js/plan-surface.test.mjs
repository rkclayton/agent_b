import assert from "node:assert/strict";
import test from "node:test";
import { claimHint, hintTexts, noteReports, planRows, proposalKey, proposalLabel } from "./plan-surface.js";

test("plan rows use only leading markers and preserve current order", () => {
  const value=planRows("# P\n## Current work order\nDo this\n## Items\n[x] 2t done\n[~] 2u live\n[ ] 3 next\n[!] 4 blocked\n[-] 5 dropped");
  assert.equal(value.order,"Do this"); assert.deepEqual(value.items.map((item)=>item.marker),["x","~"," ","!","-"]);
});
test("proposal persistence distinguishes reused ids with different edits",()=>{
  const first={id:"p",kind:"add",path:"plan.md",old_text:"a",new_text:"ab",item_id:"2t"};
  assert.notEqual(proposalKey(first),proposalKey({...first,new_text:"ac"}));
});
test("notes reports are heading-delimited and proposal labels are inert text",()=>{
  assert.equal(noteReports("## 2026 — v1 (2t)\nbody\n## next\nother")[0].body,"body");
  assert.equal(proposalLabel({kind:"add",path:"plan.md",item_id:"2t"}),"add · plan.md · 2t");
});
test("the four shipped hint texts persist as shown across a restart",()=>{
  assert.equal(Object.keys(hintTexts).length,4);
  const values=new Map(); const storage={getItem:(key)=>values.get(key)||null,setItem:(key,value)=>values.set(key,value)};
  for(const id of Object.keys(hintTexts)){assert.equal(claimHint(storage,id),hintTexts[id]);assert.equal(claimHint(storage,id),"");}
  for(const id of Object.keys(hintTexts)) assert.equal(claimHint(storage,id),"");
});

test("the tray shows a worker's verifier request from the thread or the worker, once, for its plan only", async () => {
  const { workerProposals } = await import("./plan-surface.js");
  const proposal = { id: "verify-2aa", kind: "verifier", path: "plan/items/2aa.md", old_text: "2aa first item", new_text: "", item_id: "2aa" };
  const job = (value) => ({ type: "notice", event: { type: "c.job", data: { question: "names no verifier", proposal: value } } });
  const sessions = {
    d1: { id: "d1", role: "d", plan_id: "p1", chat: [job(proposal), { type: "notice", event: { type: "c.job", data: { question: "which db?" } } }] },
    c1: { id: "c1", role: "c", plan_id: "p1", chat: [job(proposal), job({ ...proposal, id: "verify-2ab", old_text: "2ab other", item_id: "2ab" })] },
    c2: { id: "c2", role: "c", plan_id: "p2", chat: [job({ ...proposal, id: "verify-9zz", item_id: "9zz" })] },
  };
  const got = workerProposals(sessions, sessions.d1);
  assert.deepEqual(got.map((value) => value.id), ["verify-2aa", "verify-2ab"]);
  assert.deepEqual(workerProposals(sessions, { id: "b1", role: "b" }), []);
  assert.deepEqual(workerProposals(sessions, { id: "d3", role: "d", plan_id: "p1", chat: [job({ ...proposal, kind: "reword" })] }).map((value) => value.id), ["verify-2aa", "verify-2ab"]);
});

test("an item's [[id]] is kept on the row and never shown as its text", () => {
  const plain = planRows("# P\n[ ] 2aa first item\n[x] same\n");
  const named = planRows("# P\n[ ] [[2aa]] 2aa first item\n[x] [[3]] same\n");
  assert.deepEqual(named.items.map((item) => item.text), plain.items.map((item) => item.text));
  assert.deepEqual(named.items.map((item) => item.id), ["2aa", "3"]);
  assert.equal(named.items[0].line, "[ ] [[2aa]] 2aa first item");
});
test("a new plan's order sits under its milestones, and the harness form keeps its suffix",()=>{
  const fresh=planRows("# P\n## Milestones and current order\n### Milestones\n1.0\n### Current work order\nShip it\n- [ ] [[1]] one\n## Index\nx");
  assert.equal(fresh.order,"Ship it\n- [ ] [[1]] one");
  assert.equal(planRows("# P\n## Current work order — v1\nDo this\n### W1\nstep\n## Items\n[ ] 3 next").order,"Do this\n### W1\nstep");
});

test("the Plan page highlights only what changed, fades it over an hour, and filters by name", async () => {
  const { changedLines, fadeFor, fadeWindowMS, filterPlans, markerSummary } = await import("./plan-surface.js");
  assert.deepEqual(changedLines("a\nb\n", "a\nb\nc\n"), ["c"]);
  assert.deepEqual(changedLines("- [ ] one\n", "- [x] one\n"), ["- [x] one"]);
  assert.deepEqual(changedLines("x\n", "x\nx\n"), ["x"], "a repeated line is new when it appears more often");
  assert.equal(fadeFor(0, 5), 0);
  assert.equal(fadeFor(1000, 1000), 1);
  assert.equal(fadeFor(1000, 1000 + fadeWindowMS), 0);
  assert.ok(fadeFor(1000, 1000 + fadeWindowMS / 2) > 0.49);
  const plans = [{ id: "p1", name: "Walk plan" }, { id: "p2", name: "Agent_b" }];
  assert.deepEqual(filterPlans(plans, "walk").map((plan) => plan.id), ["p1"]);
  assert.equal(filterPlans(plans, "").length, 2);
  assert.equal(markerSummary({ open: 2, done: 1, stuck: 0 }), "2 open · 1 done");
  assert.equal(markerSummary({}), "no items");
});
