import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { largestFitting, recommendationForBytes, recommendationTable, recommendationTableVersion } from "./local-recommendations.js";

const html = fs.readFileSync(new URL("../setup.html", import.meta.url), "utf8");
const script = fs.readFileSync(new URL("setup.js", import.meta.url), "utf8");
const styles = fs.readFileSync(new URL("../css/setup.css", import.meta.url), "utf8");
const detector = fs.readFileSync(new URL("../../scripts/detect-local-capabilities.ps1", import.meta.url), "utf8");
const template = JSON.parse(fs.readFileSync(new URL("../../harness.example.json", import.meta.url), "utf8"));

test("fresh setup is exactly choose, check, and ready 2xz", () => {
  assert.deepEqual(template.connections, []);
  assert.deepEqual(template.agents, []);
  for (const label of ["Set up a model", "Checking your model", "Agent_b is ready"]) assert.match(script, new RegExp(label));
  assert.match(script, /screen === 1 \? chooseScreen\(\) : screen === 2 \? checkingScreen\(\) : readyScreen\(\)/);
  assert.match(script, /\$\{screen\} OF 3/);
  assert.doesNotMatch(script, /Where is your model\?|Evaluation Harness|Run commands as a separate Windows account\?|Open Chat|\bLater\b/);
});

test("checking is one ordered four-row proof and one short setup message 2xz", () => {
  assert.match(script, /\["Server", "Model", "Context size", "Test message"\]/);
  assert.match(script, /\/models/);
  assert.match(script, /setup: true/);
  assert.match(script, /answer\.status !== "passed"/);
  assert.match(script, /checksReady\(\)[\s\S]*>Next</);
  assert.doesNotMatch(script, /\/api\/eval\/measure|\/recommended/);
});

test("missing context is offered as a positive suggestion and zero cannot advance 2xz", () => {
  assert.match(script, /const suggestedContext = 8192/);
  assert.match(script, /draft\.context = published \|\| suggestedContext/);
  assert.match(script, /contextNeedsInput = published <= 0/);
  assert.match(script, /draft\.context > 0 && checkRows\.every/);
  assert.doesNotMatch(script, /n_ctx:\s*0/);
});

test("multiple models wait for a choice and failures keep their fixed lines 2xz", () => {
  assert.match(script, /models\.length > 1/);
  assert.match(script, /state: "pick"/);
  for (const line of ["Nothing answered at that address.", "The server refused the key.", "No model is loaded on that server.", "The model did not answer."]) assert.ok(script.includes(line));
  assert.match(script, /<details><summary>details<\/summary>/);
  assert.match(script, />Try again</);
});

test("close is available throughout and cancels an active local install 2xz", () => {
  assert.match(script, /data-action="close"[^>]*>×<\/button>/);
  assert.match(script, /if \(installing\) await cancelInstall\(\)/);
  assert.match(script, /request\("\/api\/model-install", undefined, "DELETE"\)/);
  assert.match(script, /location\.href = "\/chat\?setup=skip&from=setup"/);
  assert.match(html, /class="shell-window-controls"/);
  assert.doesNotMatch(html, />Chat<|setup-chat/);
});

test("connection saves before ready and routing changes only on Finish 2xz", () => {
  assert.match(script, /await saveCheckedConnection\(\);\s*screen = 3/);
  assert.match(script, /if \(!agent\.b\) agent\.b = connectionID/);
  assert.match(script, /else if \(agent\.b !== connectionID && !agent\.c\) agent\.c = connectionID/);
  assert.doesNotMatch(script, /agent\.d\s*=/);
  assert.match(script, /if \(action === "finish"\) return finish\(\)/);
  assert.match(script, /const patch = \{ agents: savedConnectionAgents\(\) \}/);
});

test("Settings setup omits diagnostics while first run offers it 2xz", () => {
  assert.match(script, /addingFromSettings = new URLSearchParams\(location\.search\)\.get\("from"\) === "settings"/);
  assert.match(script, /addingFromSettings \? "" :/);
  assert.match(script, /Send anonymous diagnostics/);
  assert.match(script, /if \(!addingFromSettings\) patch\.telemetry/);
});

test("local install chooses an accelerator and sends only a catalog id", () => {
  assert.match(script, /\/api\/model-install/);
  assert.match(script, /model_id: model\.id, backend: localBackend\(detection\)\.backend/);
  assert.match(script, /accelerators\?\.cuda/);
  assert.match(script, /accelerators\?\.vulkan/);
  assert.match(script, /gpu\?\.vram_bytes \|\| report\.system_memory_bytes/);
  assert.doesNotMatch(script, /data-field="install-backend"|model_url|runtime_url|sha256:/);
});

test("versioned recommendation table stays small and memory-class keyed", () => {
  assert.equal(recommendationTableVersion, "2026-09-07");
  assert.ok(recommendationTable.length <= 20);
  assert.equal(recommendationForBytes(8 * 1024 ** 3).model, "qwen3.5:4b");
  assert.equal(recommendationForBytes(32 * 1024 ** 3).model, "qwen3.5:27b");
  assert.equal(recommendationForBytes(128 * 1024 ** 3).model, "qwen3.5:122b");
});

test("local detection stays read-only and covers supported local runtimes", () => {
  for (const name of ["Ollama", "LM Studio", "llama-server", "python", "node", "go", "dotnet"]) assert.match(detector, new RegExp(`name='${name}'`));
  assert.doesNotMatch(detector, /\$env:Path\s*=|Start-Process|Invoke-WebRequest|Invoke-RestMethod/);
  assert.match(detector, /accelerators/);
  assert.match(detector, /vulkan-1\.dll/);
});

test("setup uses the console palette and has no motion", () => {
  assert.doesNotMatch(styles, /#[0-9a-f]{3,8}/i);
  assert.match(styles, /prefers-reduced-motion:reduce/);
  assert.match(styles, /transition-duration:0ms!important;animation-duration:0ms!important/);
});

test("the install choice is the largest catalog model that fits", () => {
  const models = [["0.8b", 0], ["2b", 4], ["4b", 6], ["9b", 12], ["27b", 20], ["35b", 48]].map(([id, min_gib]) => ({ id, min_gib }));
  const fits = (available) => models.filter((item) => !available || available >= item.min_gib);
  for (const [available, id] of [[8, "4b"], [12, "9b"], [24, "27b"], [3, "0.8b"]]) assert.equal(largestFitting(fits(available), available)?.id, id);
  assert.equal(largestFitting(fits(0), 0), undefined);
  assert.match(script, /largestFitting\(catalog\.models\.filter/);
});
