import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";
import { requireProductIcon, stagedToRemove, windowsPowerShellEnvironment, workerScratch, workerScratchRoot } from "./stage-candidate.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

function iconSizes(bytes) {
  assert.equal(bytes.readUInt16LE(0), 0);
  assert.equal(bytes.readUInt16LE(2), 1);
  const count = bytes.readUInt16LE(4), sizes = [];
  for (let index = 0; index < count; index++) {
    const at = 6 + index * 16;
    sizes.push(bytes[at] || 256);
  }
  return sizes;
}

test("the approved robot icon is copied whole and carries every Windows size 2u5", () => {
  const design = fs.readFileSync(path.join(repository, "plan", "designs", "2u5", "Agent_b.ico"));
  assert.deepEqual(iconSizes(design), [16, 20, 24, 32, 40, 48, 64, 128, 256]);
  assert.deepEqual(fs.readFileSync(path.join(repository, "cmd", "harness", "Agent_b.ico")), design);
  assert.deepEqual(fs.readFileSync(path.join(repository, "web", "assets", "Agent_b.ico")), design);
  const mark = Buffer.from(fs.readFileSync(path.join(repository, "plan", "designs", "2u5", "icon-windows.svg.b64"), "utf8"), "base64");
  assert.deepEqual(fs.readFileSync(path.join(repository, "web", "assets", "mark.svg")), mark);
  const installer = fs.readFileSync(path.join(repository, "scripts", "install-Agent_b.ps1"), "utf8");
  assert.equal((installer.match(/ShellIconRefresh\]::SHChangeNotify/g) || []).length, 1);
  assert.match(installer, /SHChangeNotify\(0x08000000, 0,/);
});

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
