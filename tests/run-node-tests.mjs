#!/usr/bin/env node

import { spawnSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { gateArm, summarizeGate } from "./gate-result.mjs";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const roots = [path.join(root, "web", "js"), path.join(root, "tools"), path.join(root, "tests")];
const files = [];
function collect(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) collect(full);
    else if (entry.name.endsWith(".test.mjs")) files.push(full);
  }
}
for (const directory of roots) collect(directory);
files.sort();
const result = spawnSync(process.execPath, ["--test", ...files], { cwd: root, stdio: "inherit" });
const status = result.status ?? 1;
process.stdout.write(`${JSON.stringify(summarizeGate([gateArm("node", status === 0 ? "pass" : "product", `exit ${status}`)]))}\n`);
process.exit(status);
