import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { agentKey, compactionFigures, lifetimeRows, runProfile, runProfileRows } from "./panel-lifetime.js";

test("Console agent selector keys named objects", () => {
  assert.equal(agentKey({ name: "Home Coder" }), "home-coder");
});

test("Live compaction figures distinguish compactions from summary model calls", () => {
  assert.equal(compactionFigures({ compaction_count: 7, compaction_model_calls: 3 }), "7 compactions · 3 summaries");
  assert.equal(compactionFigures(), "0 compactions · 0 summaries");
});

test("Lifetime rows expose all six per-brief reliability fields", () => {
  const rows = new Map(lifetimeRows({ runs: 2, turns: 4, prompt_tokens: 100, completion_tokens: 20, cached_tokens: 50, worker_reliability: { briefs: 2, completed: 1, interventions: 2, reworked: 1, silent: 1, model_failures: 1, harness_failures: 2, brief_failures: 3 } }));
  for (const label of ["completion rate", "interventions / brief", "cost / brief", "failure attribution model | harness | brief", "rework rate", "silence rate"]) assert.ok(rows.has(label), label);
  assert.equal(rows.get("failure attribution model | harness | brief"), "1 | 2 | 3");
});

test("Console body exposes selectors tools lifetime instruments and maintenance together", () => {
  const html = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  const script = fs.readFileSync(new URL("app.js", import.meta.url), "utf8");
  // Item 2iq (a): panel-agent-vision is built per row now, beside the connection
  // it describes, so it is no longer a fixed element in the markup.
  for (const value of ["panel-agent", "panel-roles", "panel-tools", "panel-stats", "clear-stats", "flush-memory", "panel-live", "panel-live-compactions", "panel-live-content", "panel-maintenance-title"]) assert.match(html, new RegExp(value));
  assert.match(script, /vision === "reads images" \? "reads" : "does-not-read"/);
  assert.match(script, /visionFinding \|\| `vision: \$\{vision\}`/);
  assert.doesNotMatch(html + script, /panel-closed|renderClosed|deleteChat/);
  // Item 2ip (b): the apparatus shows while a run is live, or when the operator
  // opens it -- not merely because a chat is selected.
  assert.match(script, /liveContent.hidden = !showApparatus/);
  assert.match(script, /liveIdle.hidden = !hasSelectedChat/);
  assert.doesNotMatch(script, /lifetime\.hidden|live\.hidden/);
  assert.match(script, /patchEndedRun/);
  assert.match(script, /if \(next\) setActive\(next\.id\)/);
});

// Item 2ji (c): lifetime beside the last twenty runs. One column cannot show that
// a connection has got worse, and that is the question this table is asked.
test("2ji: the per-connection row shows lifetime and the last twenty side by side", () => {
  // Twenty-two runs. The first two were fast and clean; the last twenty were slow
  // and spent their time waiting. Lifetime alone would blur the two together.
  const recent = [];
  for (let index = 0; index < 20; index += 1) {
    recent.push({ total_ms: 100_000, model_ms: 20_000, tool_ms: 0, waiting_ms: 80_000, tool_calls: 10, tool_failures: 2, empty_replies: 1, repeated_calls: 0 });
  }
  const counters = {
    run_time_ms: [1_000, 1_000, ...recent.map((run) => run.total_ms)],
    run_model_ms: 2 * 900 + 20 * 20_000,
    run_tool_ms: 0,
    run_waiting_ms: 20 * 80_000,
    empty_replies: 20,
    repeated_calls: 0,
    tools: { read_file: { calls: 202, failures: 40 } },
    recent_runs: recent,
  };
  const rows = runProfileRows(counters);
  const byLabel = Object.fromEntries(rows.map(([label, life, last]) => [label, { life, last }]));

  assert.equal(byLabel["runs measured"].life, "22");
  assert.equal(byLabel["runs measured"].last, "20");
  // The median is a real median: twenty runs at 100 s and two at 1 s.
  assert.equal(byLabel["median run"].life, "1.7 min");
  assert.equal(byLabel["median run"].last, "1.7 min");
  // Three percentages, in the order the item names them.
  assert.equal(byLabel["model / tools / waiting %"].last, "20/0/80");
  assert.equal(byLabel["empty replies / run"].last, "100.0%");
  assert.equal(byLabel["tool errors"].last, "20.0%");
});

test("2ji: a ledger written before this item shows no run profile at all", () => {
  assert.deepEqual(runProfileRows({}), []);
  assert.deepEqual(runProfileRows({ runs: 40, wall_ms: 900_000, tools: { shell: { calls: 5 } } }), []);
});

test("2ji: a rate with no denominator reads as absent, not as zero", () => {
  const rows = runProfileRows({ run_time_ms: [5_000], recent_runs: [{ total_ms: 5_000 }] });
  const byLabel = Object.fromEntries(rows.map(([label, life, last]) => [label, { life, last }]));
  // No tool ever ran, so there is no tool-error rate. "0.0%" would be a claim.
  assert.equal(byLabel["tool errors"].life, "—");
  assert.equal(byLabel["tool errors"].last, "—");
  // And a run that reported a wall clock but no buckets has no shares.
  assert.equal(byLabel["model / tools / waiting %"].last, "0/0/0");
});

test("2ji: the recent side sums the window and the lifetime side sums everything", () => {
  const counters = {
    run_time_ms: [1_000, 2_000, 3_000],
    run_model_ms: 600,
    run_waiting_ms: 0,
    recent_runs: [{ total_ms: 3_000, model_ms: 300, tool_calls: 1, tool_failures: 0 }],
  };
  assert.equal(runProfile(counters, false).runs, 3);
  assert.equal(runProfile(counters, false).median, 2_000);
  assert.equal(runProfile(counters, true).runs, 1);
  assert.equal(runProfile(counters, true).median, 3_000);
});
