#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
export const rules = [
  "While Agent_b runs it owns exactly one window; no console and no second window from any way of starting it",
  "Nobody picks a folder: no picker, no folder control, no typed path; the word “workspace” appears in nothing he sees",
  "The agent cannot reach its own control plane or rewrite itself",
  "Nothing in the product, its fixtures or its public pages is specific to a client",
  "Nothing pushed names a model, tool or vendor; commits are his identity only",
  "No button, menu, toggle or field exists that he did not ask for in his own words",
  "Unattended mode never asks; a would-be prompt is a failure of the item",
  "Six colours, dark, no nested and no horizontal scrolling",
  "Nothing the product, its installer or its tests start takes his screen or his focus",
  "Installing and updating never need elevation, and signing never lands on a user",
  "Anonymous data is content-free and off when its switch is off",
  "A per-event path costs the same however much is stored; no file work on the UI thread; nothing grows without bound",
];

export function parseInvariants(text) {
  const rows = [...text.matchAll(/^I(\d+) — (.*?) — (.*?) — `([^`]+)`$/gm)].map((match) => ({ id: `I${match[1]}`, rule: match[2], said: match[3], test: match[4] }));
  if (rows.length !== 12) throw new Error(`expected 12 invariant lines, found ${rows.length}`);
  rows.forEach((row, index) => {
    if (row.id !== `I${index + 1}`) throw new Error(`expected I${index + 1}, found ${row.id}`);
    if (row.rule !== rules[index]) throw new Error(`${row.id} rule text changed`);
    const relative = row.test.split(":")[0];
    if (!fs.existsSync(path.join(root, relative))) throw new Error(`${row.id} test is missing: ${relative}`);
  });
  return rows;
}

export function validateRun(rows, results) {
  for (const row of rows) {
    const result = results.find((entry) => entry.id === row.id);
    if (!result) throw new Error(`${row.id} did not run`);
    if (result.status !== "passed") throw new Error(`${row.id} ${result.status}`);
    if (/(?:# SKIP|--- SKIP|not exercised:|NOT EXERCISED|no tests to run)/i.test(result.detail ?? "")) throw new Error(`${row.id} was skipped or not exercised`);
  }
  return true;
}

const go = path.join(root, ".tools", "go", "bin", "go.exe");
const node = process.execPath;
const definitions = (candidate) => [
  ["powershell.exe", ["-NonInteractive", "-NoProfile", "-WindowStyle", "Hidden", "-ExecutionPolicy", "Bypass", "-File", path.join(root, "tests", "test-one-window-invariant.ps1"), "-Exe", path.join(candidate, "Agent_b.exe"), "-Setup", path.join(candidate, "Agent_b-setup.exe"), "-ApplicationRoot", candidate, "-RepositoryExe", path.join(root, "Agent_b.exe"), "-RepositoryRoot", root]],
  [node, ["--test", path.join(root, "web/js/operator-language.test.mjs"), path.join(root, "web/js/workspace-settings.test.mjs")]],
  [go, ["test", "-v", "./internal/web", "./internal/workspace", "-run", "TestControlPlaneRequiresBrowserSession2jy|TestForbiddenPolicyCapabilitiesStayOperatorFacingAndComplete"]],
  [node, ["--test", "--test-name-pattern=client-terms", path.join(root, "tests/docs-terms.test.mjs")]],
  [node, ["--test", "--test-name-pattern=public commits|model vendor", path.join(root, "tools/check-invariants.test.mjs"), path.join(root, "tests/docs-terms.test.mjs")]],
  [node, ["--test", path.join(root, "web/js/settings-density.test.mjs"), path.join(root, "web/js/settings-row-actions.test.mjs")]],
  [go, ["test", "-v", "./internal/agent", "-run", "TestUnattendedRefusesEveryCardKindAndRecordsIt"]],
  [node, ["--test", path.join(root, "web/js/shell-contract.test.mjs")]],
  [node, ["--test", "--test-name-pattern=no spawn site", path.join(root, "tests/docs-terms.test.mjs")]],
  [go, ["test", "-v", "./cmd/harness", "./internal/signing", "-run", "TestStartupElevationGuard|TestEverySigningPathChecksUIPolicyBeforePrivateKeyUse"]],
  [go, ["test", "-v", "./internal/telemetry", "-run", "TestTheCountsAreNotQueuedWhenTelemetryIsOff2lx|TestCrashTreeHasAClosedContentFreeShape2p7"]],
  ["powershell.exe", ["-NonInteractive", "-NoProfile", "-WindowStyle", "Hidden", "-Command", `$ErrorActionPreference='Stop'; & '${go}' test -v ./internal/telemetry ./internal/projection ./internal/events ./internal/broker ./internal/attachment ./internal/web -run 'TestBatchesAreBounded2jg|TestEveryPatchArrivesWhileAClientPollsDuringARun|TestWorkbookIngestIsBoundedAgainstMergesAndSparseCells|TestLiveProjectionPerEventCostDoesNotGrowWithStoredEvents2pd|TestLogRetentionBoundsCountAndAgeAndKeepsNewestKind2pd|TestHandledRequestDeduplicationIsBounded2pd|TestAttachmentStorageSearchDoesNotGrowWithStoredAttachments2pt'; if ($LASTEXITCODE) { exit $LASTEXITCODE }; & '${node}' --test --test-name-pattern='projection patch cost stays constant' web/js/bus.test.mjs; exit $LASTEXITCODE`]],
];

export function runGate(candidate) {
  const rows = parseInvariants(fs.readFileSync(path.join(root, "INVARIANTS.md"), "utf8"));
  const results = definitions(candidate).map(([command, args], index) => {
    const run = spawnSync(command, args, { cwd: root, encoding: "utf8", windowsHide: true, timeout: 600000 });
    const detail = `${run.stdout ?? ""}${run.stderr ?? ""}`.trim();
    process.stdout.write(`${rows[index].id} ${detail}\n`);
    const badDisposition = /(?:# SKIP|--- SKIP|not exercised:|NOT EXERCISED|no tests to run)/i.test(detail);
    return { id: rows[index].id, status: run.status === 0 && !badDisposition ? "passed" : badDisposition ? "skipped" : `failed (exit ${run.status ?? "none"})`, detail };
  });
  validateRun(rows, results);
  fs.writeFileSync(path.join(candidate, "invariants-result.json"), `${JSON.stringify({ schema: 1, commit: spawnSync("git", ["rev-parse", "HEAD"], { cwd: root, encoding: "utf8", windowsHide: true }).stdout.trim(), results: results.map(({ id, status }) => ({ id, status })) }, null, 2)}\n`);
  process.stdout.write("invariants: all held\n");
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const at = process.argv.indexOf("--candidate");
  if (at < 0 || !process.argv[at + 1]) { process.stderr.write("--candidate is required\n"); process.exit(2); }
  try { runGate(path.resolve(process.argv[at + 1])); } catch (error) { process.stderr.write(`${error.message}\n`); process.exit(1); }
}
