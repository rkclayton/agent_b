import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseInvariants, validateRun } from "./check-invariants.mjs";
import { scanTextEntries } from "./privacy-gate.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const rows = parseInvariants(fs.readFileSync(path.join(root, "INVARIANTS.md"), "utf8"));

test("the thirteen invariant lines name unchanged rules and existing tests", () => assert.equal(rows.length, 13));

test("each missing, skipped, or not-exercised invariant fails by its own id", () => {
  const green = rows.map(({ id }) => ({ id, status: "passed", detail: "ran on an invisible or headless test surface" }));
  assert.equal(validateRun(rows, green), true);
  for (const row of rows) {
    assert.throws(() => validateRun(rows, green.filter(({ id }) => id !== row.id)), new RegExp(`${row.id} did not run`));
    assert.throws(() => validateRun(rows, green.map((entry) => entry.id === row.id ? { ...entry, status: "skipped", detail: "not exercised" } : entry)), new RegExp(`${row.id} skipped`));
  }
});

test("the privacy gate catches each general class without returning the planted bytes", () => {
  const planted = [
    ["C:", "Users", "NotAStandIn", "project"].join("\\"),
    ["person", "private.invalid"].join("@"),
    [100, 65, 2, 3].join("."),
    ["guarded", "invented", "term"].join("-"),
  ];
  const findings = scanTextEntries([{ name: "fixture.txt", text: planted.join("\n") }], [{ term: planted[3], listLine: 1 }]);
  assert.deepEqual(findings.map(({ rule }) => rule).sort(), ["email", "outside-list", "private-or-shared-address", "windows-home-path"]);
  assert.doesNotMatch(JSON.stringify(findings), new RegExp(planted.map((value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|"), "i"));
});

test("the privacy gate admits only the documented stand-ins", () => {
  const text = "acme\\project\nsomeone@example.org\n192.168.1.10\n100.64.0.10\n";
  assert.deepEqual(scanTextEntries([{ name: "fixture.txt", text }]), []);
});

test("public commits carry only the pinned release identity and no attribution trailers", () => {
  const [name, email] = execFileSync("git", ["show", "-s", "--format=%an%x1f%ae", "v1.54.0^{}"], { cwd: root, encoding: "utf8", windowsHide: true }).trim().split("\x1f");
  const log = execFileSync("git", ["log", "v1.54.0..HEAD", "--format=%an%x1f%ae%x1f%B%x1e"], { cwd: root, encoding: "utf8", windowsHide: true });
  for (const record of log.split("\x1e").filter((value) => value.trim())) {
    const [author, address, message = ""] = record.trim().split("\x1f");
    assert.equal(author, name); assert.equal(address, email);
    assert.doesNotMatch(message, /Co-Authored-By|Generated with|Signed-off-by/i);
  }
});
