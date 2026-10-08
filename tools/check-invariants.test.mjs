import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { parseInvariants, validateRun } from "./check-invariants.mjs";
import { scanBinary, scanTextEntries } from "./privacy-gate.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const rows = parseInvariants(fs.readFileSync(path.join(root, "INVARIANTS.md"), "utf8"));

test("the fourteen invariant lines name unchanged rules and existing tests", () => assert.equal(rows.length, 14));

test("I14 accepts one usable release response and refuses HTTP or missing assets", async () => {
  const module = await import("./check-invariants.mjs");
  assert.equal(typeof module.checkReleaseSource, "function");
  let requests = 0;
  const good = await module.checkReleaseSource("https://example.invalid/latest", async () => {
    requests++;
    return { ok: true, status: 200, json: async () => ({ tag_name: "v9.9.9", assets: [{ name: "release.json" }, { name: "Agent_b-setup.exe" }] }) };
  });
  assert.deepEqual(good, { status: 200, tag: "v9.9.9" });
  await assert.rejects(module.checkReleaseSource("https://api.github.com/repos/someone/agent_b/releases/latest", async () => ({ ok: false, status: 404 })), /release source refused: HTTP 404/);
  await assert.rejects(module.checkReleaseSource("https://example.invalid/latest", async () => ({ ok: true, status: 200, json: async () => ({ tag_name: "v9.9.9", assets: [] }) })), /release source refused: latest release is missing release.json or Agent_b-setup.exe/);
  assert.equal(requests, 1);
});

test("the repository identity is allowed only in the updater's one address", () => {
  const origin = execFileSync("git", ["remote", "get-url", "origin"], { cwd: root, encoding: "utf8", windowsHide: true }).trim();
  const match = origin.match(/github\.com[/:]([^/]+)\/([^/]+?)(?:\.git)?$/);
  assert.ok(match, "origin must name one GitHub repository");
  const [, owner, repository] = match;
  const address = `https://api.github.com/repos/${owner}/${repository}/releases/latest`;
  const terms = [{ term: owner.toLowerCase(), listLine: 1 }];
  assert.deepEqual(scanTextEntries([{ name: "internal/updater/manager.go", text: `const LatestReleaseURL = ${JSON.stringify(address)}` }], terms), []);
  for (const name of ["internal/other.go", "docs/other.md", "release-notes/v9.9.9.md", "commit-messages:fixture"]) {
    const findings = scanTextEntries([{ name, text: address }], terms);
    assert.equal(findings.length, 1, `${name} did not refuse the repository identity`);
    assert.doesNotMatch(JSON.stringify(findings), new RegExp(owner, "i"));
  }
});

test("candidate bytes allow only the complete updater address", () => {
  const origin = execFileSync("git", ["remote", "get-url", "origin"], { cwd: root, encoding: "utf8", windowsHide: true }).trim();
  const match = origin.match(/github\.com[/:]([^/]+)\/([^/]+?)(?:\.git)?$/);
  assert.ok(match, "origin must name one GitHub repository");
  const [, owner, repository] = match;
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-repository-bytes-"));
  try {
    const allowed = path.join(directory, "allowed.bin"), refused = path.join(directory, "refused.bin");
    fs.writeFileSync(allowed, `https://api.github.com/repos/${owner}/${repository}/releases/latest`);
    fs.writeFileSync(refused, `not an update address: ${owner}`);
    const terms = [{ term: owner.toLowerCase(), listLine: 1 }];
    assert.deepEqual(scanBinary([allowed], terms), []);
    const findings = scanBinary([refused], terms);
    assert.equal(findings.length, 1);
    assert.doesNotMatch(JSON.stringify(findings), new RegExp(owner, "i"));
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

test("a clean source archive keeps the one binary exception without git metadata", () => {
  const origin = execFileSync("git", ["remote", "get-url", "origin"], { cwd: root, encoding: "utf8", windowsHide: true }).trim();
  const match = origin.match(/github\.com[/:]([^/]+)\/([^/]+?)(?:\.git)?$/);
  assert.ok(match, "origin must name one GitHub repository");
  const [, owner, repository] = match, directory = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-clean-archive-"));
  try {
    fs.mkdirSync(path.join(directory, "tools"), { recursive: true });
    fs.mkdirSync(path.join(directory, "internal", "updater"), { recursive: true });
    fs.copyFileSync(path.join(root, "tools", "privacy-gate.mjs"), path.join(directory, "tools", "privacy-gate.mjs"));
    const address = `https://api.github.com/repos/${owner}/${repository}/releases/latest`;
    fs.writeFileSync(path.join(directory, "internal", "updater", "manager.go"), `package updater\n\nconst LatestReleaseURL = ${JSON.stringify(address)}\n`);
    fs.writeFileSync(path.join(directory, "candidate.bin"), address);
    fs.writeFileSync(path.join(directory, "terms.txt"), `${owner}\n`);
    const output = execFileSync(process.execPath, [path.join(directory, "tools", "privacy-gate.mjs"), "--binary", path.join(directory, "candidate.bin")], {
      cwd: directory, encoding: "utf8", windowsHide: true, env: { ...process.env, AGENTB_CLIENT_TERMS: path.join(directory, "terms.txt") },
    });
    assert.match(output, /findings=0/);
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
});

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

test("the privacy gate catches a planted path name without returning it", () => {
  const planted = ["guarded", "path", "term"].join("-");
  const findings = scanTextEntries(
    [{ name: `fixtures/${planted}/clean.txt`, text: "clean" }],
    [{ term: planted, listLine: 1 }],
  );
  assert.deepEqual(findings.map(({ name, rule }) => ({ name, rule })), [{ name: "path-name", rule: "outside-list" }]);
  assert.doesNotMatch(JSON.stringify(findings), new RegExp(planted, "i"));
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
