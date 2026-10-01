import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseInvariants, validateRun } from "./check-invariants.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const rows = parseInvariants(fs.readFileSync(path.join(root, "INVARIANTS.md"), "utf8"));

test("the twelve invariant lines name unchanged rules and existing tests", () => assert.equal(rows.length, 12));

test("each missing, skipped, or not-exercised invariant fails by its own id", () => {
  const green = rows.map(({ id }) => ({ id, status: "passed", detail: "ran on an invisible or headless test surface" }));
  assert.equal(validateRun(rows, green), true);
  for (const row of rows) {
    assert.throws(() => validateRun(rows, green.filter(({ id }) => id !== row.id)), new RegExp(`${row.id} did not run`));
    assert.throws(() => validateRun(rows, green.map((entry) => entry.id === row.id ? { ...entry, status: "skipped", detail: "not exercised" } : entry)), new RegExp(`${row.id} skipped`));
  }
});

test("public commits carry only the configured operator identity and no attribution trailers", () => {
  const name = execFileSync("git", ["config", "user.name"], { cwd: root, encoding: "utf8", windowsHide: true }).trim();
  const email = execFileSync("git", ["config", "user.email"], { cwd: root, encoding: "utf8", windowsHide: true }).trim();
  const log = execFileSync("git", ["log", "v1.54.0..HEAD", "--format=%an%x1f%ae%x1f%B%x1e"], { cwd: root, encoding: "utf8", windowsHide: true });
  for (const record of log.split("\x1e").filter((value) => value.trim())) {
    const [author, address, message = ""] = record.trim().split("\x1f");
    assert.equal(author, name); assert.equal(address, email);
    assert.doesNotMatch(message, /Co-Authored-By|Generated with|Signed-off-by/i);
  }
});
