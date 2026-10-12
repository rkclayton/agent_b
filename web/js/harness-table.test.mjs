import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2ih (d) and (e): what the operator actually reads after a measurement.
//
// The module reaches for page context at import time, so this reads its source
// — the convention the other settings contract tests follow.
const source = fs.readFileSync(new URL("./settings-connections.js", import.meta.url), "utf8");

test("the measurement surface shows one run, speed and the window 2tp", () => {
  const fn = source.slice(source.indexOf("export function renderMeasurement"));
  assert.ok(fn.length > 0, "renderMeasurement is not exported");
  for (const shown of ["passed", "tool_errors", "completion_ms", "n_ctx", "window_tokens"]) {
	assert.ok(fn.includes(shown), `the result does not show ${shown}`);
  }
  for (const gone of ["reasoning_on", "reasoning_off", "decision?.line", "thinking on", "thinking off"]) assert.ok(!fn.includes(gone), `the old two-arm result still reads ${gone}`);
});

test("an older measurement still renders its common results", () => {
  const fn = source.slice(source.indexOf("export function renderMeasurement"));
  assert.ok(fn.includes("passed</li>"), "the common pass result was dropped");
  assert.ok(fn.includes("tool errors</li>"), "the common tool-error result was dropped");
});
