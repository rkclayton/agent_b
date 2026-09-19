#!/usr/bin/env node
// rotate-evidence (item 1c): logs/evidence keeps the five newest orders'
// directories and every directory an open item cites; older order directories
// go. It lists by default and removes only with --apply, which is the
// operator's step: hard stop (3) still governs the worker. Directories whose
// name names no release (plan-snapshots, dated one-offs) are never touched.
//
//   node scripts/rotate-evidence.mjs [--root <repo>] [--keep 5] [--apply --yes]
//
// --apply removes only with --yes beside it: there is no prompt, so the run can
// be unattended, and --apply alone refuses rather than asking.

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));

function version(name) {
  const match = name.match(/v(\d+)\.(\d+)\.(\d+)/);
  return match ? match.slice(1, 4).map(Number) : null;
}

function compare(a, b) {
  return a[0] - b[0] || a[1] - b[1] || a[2] - b[2];
}

export function evidencePlan({ root, keep = 5 }) {
  const evidence = path.join(root, "logs", "evidence");
  const names = fs.existsSync(evidence) ? fs.readdirSync(evidence).filter((name) => fs.statSync(path.join(evidence, name)).isDirectory()) : [];
  const cited = new Set();
  const items = path.join(root, "plan", "items");
  if (fs.existsSync(items)) {
    for (const file of fs.readdirSync(items).filter((name) => name.endsWith(".md"))) {
      const text = fs.readFileSync(path.join(items, file), "utf8").replaceAll("\\", "/");
      for (const match of text.matchAll(/logs\/evidence\/([^/\s`'")\]]+)/g)) cited.add(match[1]);
    }
  }
  const releases = [...new Set(names.map(version).filter(Boolean).map((value) => value.join(".")))].map((value) => value.split(".").map(Number)).sort(compare);
  const kept = new Set(releases.slice(-keep).map((value) => value.join(".")));
  const plan = { keepReleases: [...kept].map((value) => `v${value}`), keep: [], cited: [], unversioned: [], remove: [] };
  for (const name of names.sort()) {
    const value = version(name);
    if (!value) plan.unversioned.push(name);
    else if (kept.has(value.join("."))) plan.keep.push(name);
    else if (cited.has(name)) plan.cited.push(name);
    else plan.remove.push(name);
  }
  return plan;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  let root = path.resolve(here, "..");
  let keep = 5;
  let apply = false;
  let yes = false;
  for (let index = 2; index < process.argv.length; index += 1) {
    const argument = process.argv[index];
    if (argument === "--root" && process.argv[index + 1]) root = path.resolve(process.argv[++index]);
    else if (argument === "--keep" && process.argv[index + 1]) keep = Number(process.argv[++index]);
    else if (argument === "--apply") apply = true;
    else if (argument === "--yes") yes = true;
    else throw new Error(`unknown argument: ${argument}`);
  }
  if (!Number.isInteger(keep) || keep < 1) throw new Error("--keep must be a positive integer");
  if (apply && !yes) throw new Error("--apply removes evidence; add --yes to confirm");
  const plan = evidencePlan({ root, keep });
  process.stdout.write(`${JSON.stringify({ ...plan, applied: apply }, null, 2)}\n`);
  if (apply) {
    const evidence = path.join(root, "logs", "evidence");
    for (const name of plan.remove) removeTreeWithinAllowedRoots(path.join(evidence, name), [evidence], "evidence rotation");
  }
}
