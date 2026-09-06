import assert from "node:assert/strict";
import test from "node:test";
import { approvalChoices, shellGrantApproval } from "./approval.js";

test("shell approval cards expose once, run, and explicit operator mode", () => {
  for (const data of [
    { name: "shell", boundary_escape: false },
    { name: "shell.operator_override", boundary_escape: true },
    { name: "shell.operator_command", boundary_escape: true },
  ]) {
    assert.equal(shellGrantApproval(data), true);
    assert.deepEqual(approvalChoices(data).map(([value]) => value), ["once", "run", "operator_mode", "deny"]);
  }
});

test("file boundary and generic policy cards retain their narrower decisions", () => {
  assert.deepEqual(approvalChoices({ name: "write_file.operator_override", boundary_escape: true }).map(([value]) => value), ["approve", "deny"]);
  assert.deepEqual(approvalChoices({ name: "write_file", boundary_escape: false }).map(([value]) => value), ["approve", "deny"]);
});
