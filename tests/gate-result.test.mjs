import assert from "node:assert/strict";
import test from "node:test";
import { gateArm, summarizeGate } from "./gate-result.mjs";

test("gate summary never calls an unexercised model arm green", () => {
  const result = summarizeGate([gateArm("unit", "pass"), gateArm("model", "prerequisite unavailable", "no model")]);
  assert.deepEqual({ passed: result.passed, product_fail: result.product_fail, not_exercised: result.not_exercised, green: result.green }, { passed: 1, product_fail: 0, not_exercised: 1, green: false });
  assert.match(result.arms[1].status, /^not exercised: prerequisite unavailable$/);
});

test("product failures are counted separately", () => {
  const result = summarizeGate([gateArm("unit", "product", "assertion")]);
  assert.equal(result.product_fail, 1);
  assert.equal(result.green, false);
});
