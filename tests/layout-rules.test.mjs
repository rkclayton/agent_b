import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawnSync } from "node:child_process";
import { auditLayoutSnapshot } from "./layout-gate.mjs";

const box = (left, top, width, height) => ({ left, top, right: left + width, bottom: top + height, width, height });
const clean = () => ({
  viewport: { width: 304, height: 254 },
  elements: [
    { id: "label", box: box(8, 8, 72, 24), container: box(0, 0, 304, 254), textWhole: true },
    { id: "control", box: box(88, 8, 208, 24), container: box(0, 0, 304, 254), textWhole: true },
  ],
  scrolls: [{ id: "page", box: box(0, 0, 304, 254), horizontal: false }],
  groups: [{ id: "rows", rows: [
    { label: box(8, 8, 72, 24), control: box(88, 8, 208, 24) },
    { label: box(8, 40, 72, 24), control: box(88, 40, 208, 24) },
  ] }],
  gaps: [{ id: "rows", value: 8, compound: false }],
});

const only = (mutate) => { const value = clean(); mutate(value); return value; };
const verdict = (snapshot) => auditLayoutSnapshot(snapshot).map(({ rule }) => rule);

test("L1 refuses one cut value", () => {
  assert.deepEqual(verdict(only((x) => { x.elements[1].textWhole = false; })), ["L1"]);
});

test("L2 refuses one overlap", () => {
  assert.deepEqual(verdict(only((x) => { x.elements.push({ id: "other", box: box(200, 8, 96, 24), container: box(0, 0, 304, 254), textWhole: true }); })), ["L2"]);
});

test("L3 refuses one nested scroller", () => {
  assert.deepEqual(verdict(only((x) => { x.scrolls.push({ id: "inner", box: box(20, 20, 100, 100), horizontal: false }); })), ["L3"]);
});

test("L4 refuses one control outside its container", () => {
  assert.deepEqual(verdict(only((x) => { x.elements[1].container = box(0, 0, 200, 254); })), ["L4"]);
});

test("L5 refuses one misaligned row group", () => {
  assert.deepEqual(verdict(only((x) => { x.groups[0].rows[1].control.left += 4; x.groups[0].rows[1].control.right += 4; })), ["L5"]);
});

test("L6 refuses one off-scale gap", () => {
  assert.deepEqual(verdict(only((x) => { x.gaps[0].value = 6; })), ["L6"]);
});

test("a clean fixture passes every rule", () => {
  assert.deepEqual(auditLayoutSnapshot(clean()), []);
});

test("a release report names page size rule and element before staging", () => {
  const directory = mkdtempSync(join(tmpdir(), "agentb-layout-refusal-"));
  const report = join(directory, "report.json");
  writeFileSync(report, JSON.stringify({ results: [{ page: "Connections", size: "minimum", blocking: true, failures: [{ rule: "L1", element: "model", detail: "cut" }] }], reviews: [] }));
  const result = spawnSync(process.execPath, [resolve("tests/layout-gate.mjs"), "--release-check", report], { encoding: "utf8" });
  assert.equal(result.status, 1);
  assert.match(result.stderr, /LAYOUT REFUSED: Connections minimum L1 model/);
  const deploy = readFileSync(resolve("tools/deploy-release.ps1"), "utf8");
  assert.ok(deploy.indexOf("layout-gate.mjs") >= 0, "deploy has no layout gate");
  assert.ok(deploy.indexOf("layout-gate.mjs") < deploy.indexOf("stage-candidate.mjs"), "layout gate runs after staging");
});
