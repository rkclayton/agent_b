import assert from "node:assert/strict";
import test from "node:test";

import { operatorLogEntry } from "./operator-log.js";

test("operator mode transitions have explicit persistent log labels", () => {
  assert.deepEqual(operatorLogEntry({ enabled: true }), { text: "Operator mode enabled", alarm: true });
  assert.deepEqual(operatorLogEntry({ enabled: false, reason: "disabled by operator request" }), { text: "Operator mode disabled", alarm: false });
  assert.deepEqual(operatorLogEntry({ enabled: false, reason: "idle timeout expired" }), { text: "Operator mode disabled · idle timeout expired", alarm: false });
});
