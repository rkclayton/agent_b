import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { requireProductIcon, stagedToRemove, windowsPowerShellEnvironment, workerScratch, workerScratchRoot } from "./stage-candidate.mjs";

test("only the three newest staged versions are kept, by version and not by name", () => {
  const names = ["v0.69.0", "v0.70.0", "v0.69.1", "v0.9.0", "v0.10.0", "notes", "v1.0"];
  assert.deepEqual(stagedToRemove(names), ["v0.10.0", "v0.9.0"]);
  assert.deepEqual(stagedToRemove(["v0.69.0"]), []);
});

test("worker scratch is one folder per order under %TEMP%\\agentb-worker", () => {
  const temp = path.resolve("scratch-temp");
  assert.equal(workerScratch("v0.69.0", temp), path.join(temp, "agentb-worker", "v0.69.0"));
  assert.equal(workerScratchRoot(temp), path.join(temp, "agentb-worker"));
  for (const bad of ["", "..", "../x", "a/b", "a\\b"]) assert.throws(() => workerScratch(bad, temp));
});

test("Windows PowerShell staging receives only native module roots", () => {
  const environment = windowsPowerShellEnvironment({
    SystemRoot: "C:\\Windows",
    ProgramFiles: "C:\\Program Files",
    USERPROFILE: "C:\\Users\\Operator",
    PSModulePath: "C:\\Program Files\\PowerShell\\Modules",
  });
  assert.equal(environment.PSModulePath, [
    "C:\\Users\\Operator\\Documents\\WindowsPowerShell\\Modules",
    "C:\\Program Files\\WindowsPowerShell\\Modules",
    "C:\\Windows\\System32\\WindowsPowerShell\\v1.0\\Modules",
  ].join(path.win32.delimiter));
});

test("staging refuses a candidate without the product icon", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-icon-test-"));
  try {
    assert.throws(() => requireProductIcon(root), /staged product icon is missing/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
