// Prove `prepare --dry-run` reports the same diagnostic each of the three
// 2026-09-15 round trips cost, against the retained evidence, writing nothing.
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";

const root = process.cwd();
const scratch = fs.mkdtempSync(path.join(os.tmpdir(), "planlint-dryrun-"));
const out = [];

function say(line) { out.push(line); console.log(line); }

function dryRun({ bodyFile, itemOverrides = {}, removeFiles = [] }) {
  // Run against a throwaway copy of the live plan state so a shape can be
  // reproduced without touching plan/ at all.
  const work = fs.mkdtempSync(path.join(scratch, "root-"));
  fs.copyFileSync(path.join(root, "PLAN.md"), path.join(work, "PLAN.md"));
  for (const label of ["items", "archive"]) {
    const from = path.join(root, "plan", label);
    const to = path.join(work, "plan", label);
    fs.mkdirSync(to, { recursive: true });
    for (const name of fs.readdirSync(from)) fs.copyFileSync(path.join(from, name), path.join(to, name));
  }
  for (const relative of removeFiles) {
    fs.rmSync(path.join(work, relative), { force: true });
  }
  for (const [relative, text] of Object.entries(itemOverrides)) {
    fs.writeFileSync(path.join(work, relative), text, "utf8");
  }
  const before = snapshot(work);
  const args = ["scripts/plan-publish.mjs", "prepare", "--dry-run", "--root", work];
  if (bodyFile) args.push("--body", bodyFile);
  let stdout = "", code = 0;
  try {
    stdout = execFileSync("node", args, { cwd: root, encoding: "utf8" });
  } catch (error) {
    stdout = error.stdout || "";
    code = error.status ?? 1;
  }
  const after = snapshot(work);
  return { result: JSON.parse(stdout), code, untouched: before === after };
}

function snapshot(dir) {
  const entries = [];
  const walk = (d) => {
    for (const name of fs.readdirSync(d).sort()) {
      const full = path.join(d, name);
      const stat = fs.statSync(full);
      if (stat.isDirectory()) walk(full);
      else entries.push(`${path.relative(dir, full)}:${stat.size}:${fs.readFileSync(full, "utf8").length}`);
    }
  };
  walk(dir);
  return entries.join("\n");
}

// --- Shape 1: a wrapped ## Unresolved continuation line (2al, REPO-MOVE r1).
const wrapped = fs.readFileSync(path.join(root, "plan", "archive", "2al.md"), "utf8");
const shape1Item = wrapped
  .replace("state: shipped", "state: live")
  .replace(/^## Unresolved[\s\S]*$/m, "## Unresolved\n\n- [discovery] Whether a CLA is in place;\n  a wrapped continuation line like this one is its own entry.\n");
const shape1Body = path.join(scratch, "body-2al.md");
fs.writeFileSync(shape1Body, [
  "## Current work order — dry-run shape 1",
  "",
  "Order ID: `DRYRUN-1`",
  "",
  "- W1 **2al: preserve.** a clause naming the item.",
  "",
].join("\n"), "utf8");
const one = dryRun({ bodyFile: shape1Body, itemOverrides: { "plan/items/2al.md": shape1Item }, removeFiles: ["plan/archive/2al.md"] });
say("### shape 1 — wrapped ## Unresolved continuation (2al)");
say(`  exit ${one.code}  ok=${one.result.ok}  wrote nothing=${one.untouched}`);
one.result.errors.forEach((e) => say(`  ${e}`));

// --- Shape 2: an unrecognised ## Unresolved tag ([decision] in 2cz).
const cz = fs.readFileSync(path.join(root, "plan", "items", "2cz.md"), "utf8");
const shape2Item = cz.replace("- [discovery] Operator choice", "- [decision] Operator choice");
const shape2Body = path.join(scratch, "body-2cz.md");
fs.writeFileSync(shape2Body, [
  "## Current work order — dry-run shape 2",
  "",
  "Order ID: `DRYRUN-2`",
  "",
  "- W1 **2cz: candidates only.** a clause naming the item.",
  "",
].join("\n"), "utf8");
const two = dryRun({ bodyFile: shape2Body, itemOverrides: { "plan/items/2cz.md": shape2Item } });
say("");
say("### shape 2 — [decision] tag outside the recognised set (2cz)");
say(`  exit ${two.code}  ok=${two.result.ok}  wrote nothing=${two.untouched}`);
two.result.errors.forEach((e) => say(`  ${e}`));

// --- Shape 3: a lettered W heading the executable regex used to skip (0b).
const shape3Body = path.join(scratch, "body-0b.md");
fs.writeFileSync(shape3Body, [
  "## Current work order — dry-run shape 3",
  "",
  "Order ID: `DRYRUN-3`",
  "",
  "- W1 **2cz: candidates only.** a clause naming a gated item.",
  "- W2b **0b: remove alpha.** the shape that silently skipped the gate.",
  "",
].join("\n"), "utf8");
const three = dryRun({ bodyFile: shape3Body });
say("");
say("### shape 3 — lettered W heading naming an item (0b, REPO-MOVE r2)");
say(`  exit ${three.code}  ok=${three.result.ok}  wrote nothing=${three.untouched}`);
three.result.errors.forEach((e) => say(`  ${e}`));

// --- Control: the corrected files, as published, exit zero.
const clean = dryRun({});
say("");
say("### control — the live published plan, uncorrupted");
say(`  exit ${clean.code}  ok=${clean.result.ok}  wrote nothing=${clean.untouched}  items=${clean.result.items}`);
say(`  gated items: ${clean.result.gated_items.join("  ")}`);

fs.rmSync(scratch, { recursive: true, force: true });

const failures = [];
if (one.code === 0 || !one.result.errors.some((e) => e.includes("2al"))) failures.push("shape 1 did not reproduce");
if (two.code === 0 || !two.result.errors.some((e) => e.includes("2cz"))) failures.push("shape 2 did not reproduce");
if (three.code === 0 || !three.result.errors.some((e) => e.includes("W2b"))) failures.push("shape 3 did not reproduce");
if (clean.code !== 0 || !clean.result.ok) failures.push("control did not pass");
for (const item of [one, two, three, clean]) if (!item.untouched) failures.push("a dry run wrote something");

say("");
say(failures.length ? "FAIL: " + failures.join("; ") : "PASS: all three shapes reproduce, the control passes, and no dry run wrote anything");
fs.writeFileSync(path.join(root, "logs/evidence/2026-09-15-v0.61.0/w4-dry-run-proof.txt"), out.join("\n") + "\n");
if (failures.length) process.exitCode = 1;
