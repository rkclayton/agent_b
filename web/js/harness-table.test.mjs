import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2ih (d) and (e): what the operator actually reads after a measurement.
//
// The module reaches for page context at import time, so this reads its source
// — the convention the other settings contract tests follow.
const source = fs.readFileSync(new URL("./settings-connections.js", import.meta.url), "utf8");

test("the measurement surface shows both arms, the decision and the window", () => {
  const fn = source.slice(source.indexOf("export function renderMeasurement"));
  assert.ok(fn.length > 0, "renderMeasurement is not exported");
  for (const shown of ["reasoning_on", "reasoning_off", "empty_replies", "decision?.line", "n_ctx", "window_tokens"]) {
    assert.ok(fn.includes(shown), `the table does not show ${shown}`);
  }
  // (c): the one line the harness wrote is rendered, not summarised.
  assert.ok(fn.includes("measurement.decision?.line"), "the decision line is not rendered verbatim");
});

test("a measurement from before the two arms still renders as what it was", () => {
  const fn = source.slice(source.indexOf("export function renderMeasurement"));
  // The arms are shown only when BOTH are present; otherwise the old
  // passed/tool-errors list. A single-arm record must not render as a two-arm
  // run with empty halves.
  assert.ok(fn.includes("measurement.reasoning_on && measurement.reasoning_off"), "the arms are not guarded");
  assert.ok(fn.includes("passed</li>"), "the pre-2m2 rendering was dropped");
});
