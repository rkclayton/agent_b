import assert from "node:assert/strict";
import test from "node:test";

import { operatorLogEntry } from "./operator-log.js";

test("operator mode transitions have explicit persistent log labels", () => {
  assert.deepEqual(operatorLogEntry({ enabled: true }), { text: "Run as you enabled", alarm: true });
  assert.deepEqual(operatorLogEntry({ enabled: false, reason: "disabled by user request" }), { text: "Run as you disabled", alarm: false });
  assert.deepEqual(operatorLogEntry({ enabled: false, reason: "idle timeout expired" }), { text: "Run as you disabled · idle timeout expired", alarm: false });
  // Journals written before 2pz carry the old reason; it is still the default and not shown.
  assert.deepEqual(operatorLogEntry({ enabled: false, reason: "disabled by operator request" }), { text: "Run as you disabled", alarm: false });
});
