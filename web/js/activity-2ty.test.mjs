import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { activitySummary, groupedLifetimeRows, toolUseRows } from "./panel-lifetime.js";

test("tool use sorts by calls then name and computes call share 2ty", () => {
  const tools = ["zero", "three-b", "nine", "three-a"].map((name) => ({ name }));
  const counters = { nine: { calls: 9, failures: 1 }, "three-b": { calls: 3 }, "three-a": { calls: 3 }, zero: {} };
  const rows = toolUseRows(tools, counters);
  assert.deepEqual(rows.map((row) => row.name), ["nine", "three-a", "three-b", "zero"]);
  assert.deepEqual(rows.map((row) => row.share), [60, 20, 20, 0]);
});

test("lifetime figures preserve every former figure under the four required groups 2ty", () => {
  const rows = groupedLifetimeRows({}, () => 0);
  assert.deepEqual([...new Set(rows.map((row) => row.group))], ["Runs", "Approvals", "Context", "Time"]);
  assert.equal(rows.length, 17);
  assert.ok(rows.every((row) => row.figure && row.value !== undefined));
});

test("Activity owns a profile picker, headed tables and a single clear action", () => {
  const markup = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  assert.match(markup, /id="activity-profile"/);
  for (const heading of ["Average run", "Overall"]) assert.match(markup, new RegExp(`<h3[^>]*>${heading}<`));
  assert.equal([...markup.matchAll(/id="clear-stats"/g)].length, 1);
  assert.doesNotMatch(markup, /id="panel-tool-counters"/);
});

test("Activity averages and totals use the selected profile's recorded counts", () => {
  assert.equal(activitySummary({ runs: 0 }), null);
  const result = activitySummary({
    chats: 2, runs: 10, turns: 44, wall_ms: 1_440_000, prompt_tokens: 84_100_000, completion_tokens: 4_700_000,
    tools: { read: { calls: 87, failures: 3 }, list: { calls: 9, failures: 0 } }, worker_reliability: { completed: 9 },
  });
  assert.deepEqual(result.average.map((row) => row[1]), ["2.4 min", "4.4", "8,880,000", "9.6", "3.1%", "90.0%"]);
  assert.deepEqual(result.overall.map((row) => row[1]), ["2", "10", "44", "84.1M / 4.7M", "96", "24.0 min"]);
});
