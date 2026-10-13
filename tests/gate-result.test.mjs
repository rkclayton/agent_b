import assert from "node:assert/strict";
import test from "node:test";
import { gateArm, summarizeGate, summarizeGoTestJSON } from "./gate-result.mjs";

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

test("go test JSON is summarized once per package", () => {
  const line = (Action, Package, Test) => JSON.stringify({ Action, Package, ...(Test && { Test }) });
  const result = summarizeGoTestJSON([line("run", "example/one", "TestPass"), line("pass", "example/one", "TestPass"), line("run", "example/one", "TestSkip"), line("skip", "example/one", "TestSkip"), line("pass", "example/one"), line("run", "example/two", "TestFail"), line("fail", "example/two", "TestFail"), line("fail", "example/two")].join("\n"));
  assert.deepEqual(result, [
    { package: "example/one", run: 2, skipped: 1, failed: 0, status: "pass" },
    { package: "example/two", run: 1, skipped: 0, failed: 1, status: "fail" },
  ]);
});
