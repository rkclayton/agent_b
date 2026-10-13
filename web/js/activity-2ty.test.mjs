import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { groupedLifetimeRows, toolUseRows } from "./panel-lifetime.js";

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

test("Activity owns a profile picker and headed Tool and Figure tables 2ty", () => {
  const markup = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  assert.match(markup, /id="activity-profile"/);
  for (const heading of ["Tool", "Calls", "Failed", "Last used", "Figure", "Value"])
    assert.match(markup, new RegExp(`<th[^>]*>${heading}<`));
});
