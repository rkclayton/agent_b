import assert from "node:assert/strict";
import test from "node:test";

import { runProfileRows } from "./panel-lifetime.js";

// Item 2m2 (f) at the unit level: the two-run gate check at W4 proves the
// pixels, and this proves the property the pixels depend on — that a measured
// value's SEAT does not depend on the value.
//
// The gate could only ever tell us that two runs happened to agree. This says
// why they must: the width is declared beside the formatter, from the value's
// range, and no sample of the data can change it.

const counters = (runs, ms) => ({
  run_time_ms: Array.from({ length: runs }, () => ms),
  run_model_ms: Math.round(ms * runs * 0.6),
  run_tool_ms: Math.round(ms * runs * 0.3),
  run_waiting_ms: Math.round(ms * runs * 0.1),
  empty_replies: 1,
  repeated_calls: 1,
  tools: { shell: { calls: 10, failures: 3 } },
});

test("a row's seat is the same whatever the measurement says", () => {
  const small = runProfileRows(counters(3, 50));
  const large = runProfileRows(counters(3, 21600000));
  assert.equal(small.length, large.length);
  assert.ok(small.length > 0, "no rows were produced, so this proves nothing");
  for (const [index, row] of small.entries()) {
    const [label, life, , seat] = row;
    const [otherLabel, otherLife, , otherSeat] = large[index];
    assert.equal(label, otherLabel);
    assert.equal(seat, otherSeat, `${label}: the seat moved with the value`);
    assert.ok(Number.isInteger(seat) && seat > 0, `${label}: no seat declared`);
    // The point of the seat: the values DO differ, and the width does not.
    if (label === "median run") assert.notEqual(life, otherLife, "the fixture did not actually change the measurement");
  }
});

test("every seat holds the widest form its row can produce", () => {
  // (c): sized from the range, not from today's sample. The widest value each
  // formatter can emit must fit in the seat it declared.
  const widest = {
    "runs measured": "99999",
    "median run": "21600.0 s",
    "model / tools / waiting %": "100/100/100",
    "empty replies / run": "100.0%",
    "tool errors": "100.0%",
    "repeated calls / run": "100.0%",
  };
  for (const [label, life, , seat] of runProfileRows(counters(3, 1234))) {
    assert.ok(widest[label] !== undefined, `${label} has no declared widest form; a value nobody listed is card 3 all over again`);
    assert.ok(seat >= widest[label].length, `${label}: seat ${seat} cannot hold ${widest[label]}`);
    assert.ok(String(life).length <= seat, `${label}: a real value "${life}" already overflows its seat`);
  }
});
