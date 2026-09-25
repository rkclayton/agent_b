#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(process.env.AGENTB_HEALTH_ROOT || path.resolve(path.dirname(fileURLToPath(import.meta.url)), ".."));
const textExtensions = new Set([".css", ".go", ".html", ".js", ".json", ".md", ".mjs", ".ps1", ".svg", ".txt", ".vbs", ".yml", ".yaml"]);

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { cwd: root, encoding: "utf8", windowsHide: true, maxBuffer: 128 * 1024 * 1024, ...options });
  if (result.error) throw result.error;
  return result;
}

function gitText(ref, relative) {
  if (!ref) return fs.readFileSync(path.join(root, relative), "utf8");
  const result = run("git", ["show", `${ref}:${relative.replaceAll("\\", "/")}`]);
  return result.status === 0 ? result.stdout : "";
}

function dependencyCount(goMod, packageJSON) {
  const modules = [...goMod.matchAll(/^\s*(?:require\s+)?([a-zA-Z0-9][^\s]+)\s+v[^\s]+(?:\s+\/\/ indirect)?\s*$/gm)].length;
  let node = 0;
  try {
    const value = JSON.parse(packageJSON);
    node = Object.keys(value.dependencies || {}).length + Object.keys(value.devDependencies || {}).length;
  } catch {}
  return modules + node;
}

function configKeyCount(source) {
  return new Set([...source.matchAll(/`json:"([^",-]+)(?:,[^"]*)?"`/g)].map((match) => match[1])).size;
}

function testCount(files, read) {
  let count = 0;
  for (const file of files) {
    if (file.endsWith("_test.go")) count += [...read(file).matchAll(/^func Test\w+\s*\(/gm)].length;
    if (file.endsWith(".test.mjs")) count += [...read(file).matchAll(/^test(?:\.skip)?\s*\(/gm)].length;
  }
  return count;
}

export function repositoryMetrics({ ref = "", warnings = 0 } = {}) {
  const listed = run("git", ref ? ["ls-tree", "-r", "--name-only", ref] : ["ls-files"]);
  if (listed.status !== 0) throw new Error(listed.stderr.trim() || "git file listing failed");
  const files = listed.stdout.split(/\r?\n/).filter(Boolean).filter((file) => ref || fs.existsSync(path.join(root, file)));
  const read = (relative) => gitText(ref, relative);
  const textFiles = files.filter((file) => textExtensions.has(path.extname(file).toLowerCase()));
  const loc = textFiles.reduce((total, file) => total + read(file).split(/\r?\n/).length - 1, 0);
  return {
    warnings,
    tests: testCount(files, read),
    loc,
    files: files.length,
    deps: dependencyCount(read("go.mod"), read("package.json")),
    config_keys: configKeyCount(read("internal/config/config.go")),
  };
}

function warningCount() {
  const lint = run(process.execPath, ["tools/plan-lint.mjs", "--structural"]);
  const lintCount = Number((`${lint.stdout}\n${lint.stderr}`.match(/(\d+) warning\(s\)/) || [0, 0])[1]);
  const go = process.env.AGENTB_GO || "C:\\Go\\bin\\go.exe";
  const vet = run(go, ["vet", "./..."]);
  const vetLines = `${vet.stdout}\n${vet.stderr}`.split(/\r?\n/).filter((line) => line.trim() && !/^\?\s|^ok\s/.test(line)).length;
  return lintCount + vetLines;
}

function budgetOf(text) {
  const line = text.match(/^@budget\s+(.+)$/mi)?.[1];
  if (!line) return null;
  const value = (label) => {
    const match = line.match(new RegExp(`${label}\\s*(?:≤|<=)?\\s*\\+?(\\d+)`, "i"));
    return match ? Number(match[1]) : null;
  };
  return { loc: value("net LOC"), files: value("new files?"), deps: value("new deps?"), config_keys: value("new config keys?") };
}

export function budgetResult({ item, base = "HEAD" }) {
  const relative = path.relative(root, path.resolve(item)).replaceAll("\\", "/");
  const budget = budgetOf(fs.readFileSync(path.resolve(item), "utf8"));
  if (!budget || Object.values(budget).some((value) => value === null)) return { ok: false, item: path.basename(item, ".md"), reason: "missing or incomplete @budget" };
  const diff = run("git", ["diff", "--numstat", base, "--"]);
  if (diff.status !== 0) throw new Error(diff.stderr.trim() || "git diff failed");
  let net = 0;
  for (const line of diff.stdout.split(/\r?\n/)) {
    const [add, remove] = line.split("\t");
    if (/^\d+$/.test(add) && /^\d+$/.test(remove)) net += Number(add) - Number(remove);
  }
  const names = run("git", ["diff", "--name-status", base, "--"]);
  const newFiles = names.stdout.split(/\r?\n/).filter((line) => line.startsWith("A\t")).length;
  const beforeDeps = dependencyCount(gitText(base, "go.mod"), gitText(base, "package.json"));
  const afterDeps = dependencyCount(gitText("", "go.mod"), gitText("", "package.json"));
  const beforeKeys = configKeyCount(gitText(base, "internal/config/config.go"));
  const afterKeys = configKeyCount(gitText("", "internal/config/config.go"));
  const actual = { loc: net, files: newFiles, deps: Math.max(0, afterDeps - beforeDeps), config_keys: Math.max(0, afterKeys - beforeKeys) };
  const over = Object.keys(actual).filter((key) => actual[key] > budget[key]);
  return { ok: over.length === 0, item: path.basename(relative, ".md"), base, budget, actual, over };
}

function formatHealth(metrics, tokens) {
  const parts = [`warnings ${metrics.warnings}`, `tests ${metrics.tests}`, `LOC ${metrics.loc}`, `files ${metrics.files}`, `deps ${metrics.deps}`, `config keys ${metrics.config_keys}`];
  if (tokens !== null) parts.push(`tokens this order ${tokens}`);
  return `HEALTH · ${parts.join(" · ")}${tokens === null ? " · tokens this order unavailable" : ""}`;
}

function main(argv) {
  const value = (flag) => { const index = argv.indexOf(flag); return index >= 0 ? argv[index + 1] : ""; };
  const item = value("--budget");
  if (item) {
    const result = budgetResult({ item, base: value("--base") || "HEAD" });
    if (argv.includes("--json")) console.log(JSON.stringify(result));
    else if (result.reason) console.log(`BUDGET ${result.item} STOP · ${result.reason}`);
    else console.log(`BUDGET ${result.item} ${result.ok ? "PASS" : "STOP"} · net LOC ${result.actual.loc}/${result.budget.loc} · new files ${result.actual.files}/${result.budget.files} · new deps ${result.actual.deps}/${result.budget.deps} · new config keys ${result.actual.config_keys}/${result.budget.config_keys}`);
    process.exitCode = result.ok ? 0 : 2;
    return;
  }
  const metrics = repositoryMetrics({ ref: value("--ref"), warnings: argv.includes("--no-checks") ? 0 : warningCount() });
  const tokenText = value("--tokens");
  const tokens = tokenText === "" ? null : Number(tokenText);
  console.log(argv.includes("--json") ? JSON.stringify({ ...metrics, ...(tokens === null ? {} : { tokens }) }) : formatHealth(metrics, tokens));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) main(process.argv.slice(2));
