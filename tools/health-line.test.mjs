import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { spawnSync } from "node:child_process";
import { budgetResult, repositoryMetrics } from "./health-line.mjs";

test("health metrics contain the six available repository numbers", () => {
  const metrics = repositoryMetrics({ warnings: 7 });
  assert.deepEqual(Object.keys(metrics), ["warnings", "tests", "loc", "files", "deps", "config_keys"]);
  for (const value of Object.values(metrics)) assert.equal(Number.isInteger(value), true);
});

test("a dependency exceeds a nothing-grows budget", () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-health-"));
  try {
    spawnSync("git", ["init", "-q"], { cwd: root });
    spawnSync("git", ["config", "user.email", "fixture@example.test"], { cwd: root });
    spawnSync("git", ["config", "user.name", "Fixture"], { cwd: root });
    fs.mkdirSync(path.join(root, "internal", "config"), { recursive: true });
    fs.mkdirSync(path.join(root, "plan", "items"), { recursive: true });
    fs.writeFileSync(path.join(root, "go.mod"), "module fixture\n\ngo 1.24\n");
    fs.writeFileSync(path.join(root, "package.json"), "{}\n");
    fs.writeFileSync(path.join(root, "internal", "config", "config.go"), "package config\n");
    const item = path.join(root, "plan", "items", "2x.md");
    fs.writeFileSync(item, "@budget net LOC ≤ +99, new files 9, new deps 0, new config keys 0\n");
    spawnSync("git", ["add", "."], { cwd: root });
    spawnSync("git", ["commit", "-qm", "base"], { cwd: root });
    fs.writeFileSync(path.join(root, "go.mod"), "module fixture\n\ngo 1.24\n\nrequire example.com/new v1.0.0\n");
    const script = path.resolve("tools/health-line.mjs");
    const result = spawnSync(process.execPath, [script, "--budget", item, "--base", "HEAD"], { cwd: root, encoding: "utf8", env: { ...process.env, AGENTB_HEALTH_ROOT: root } });
    assert.equal(result.status, 2);
    assert.match(result.stdout, /BUDGET 2x STOP.*new deps 1\/0/);
  } finally {
    fs.rmSync(root, { recursive: true, force: true });
  }
});
