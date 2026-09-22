import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { recommendationForBytes, recommendationTable, recommendationTableVersion } from "./local-recommendations.js";

const html = fs.readFileSync(new URL("../setup.html", import.meta.url), "utf8");
const script = fs.readFileSync(new URL("setup.js", import.meta.url), "utf8");
const styles = fs.readFileSync(new URL("../css/setup.css", import.meta.url), "utf8");
const detector = fs.readFileSync(new URL("../../scripts/detect-local-capabilities.ps1", import.meta.url), "utf8");
const template = JSON.parse(fs.readFileSync(new URL("../../harness.example.json", import.meta.url), "utf8"));

test("Fresh template has no servers and setup asks the four setup questions", () => {
  assert.deepEqual(template.servers, []);
  assert.deepEqual(template.agents, []);
  for (const label of ["Where is your model?", "Evaluation Harness", "Who does what?", "Done"]) assert.match(script, new RegExp(label.replaceAll("?", "\\?")));
  for (const label of ["Test", "Install one here", "Later", "Measure it", "Use profile for everything"]) assert.match(script, new RegExp(label));
  assert.doesNotMatch(`${html}\n${script}`, /\b(?:PKI|accounting)\b/i);
});

test("Connection Test and capability screen use the existing probe", () => {
  for (const label of ["Context", "Tools", "Images", "Reasoning", "Capability number", "unmeasured"]) assert.match(script, new RegExp(label));
  assert.match(script, /\/api\/servers\/\$\{encodeURIComponent\(profileID\)\}\/probe/);
});

test("Local install chooses accelerator and measurement requests carry only catalog IDs", () => {
  assert.match(script, /\/api\/model-install/);
  assert.match(script, /model_id: field\("install-model"\), backend: localBackend\(detection\)\.backend/);
  assert.match(script, /accelerators\?\.cuda/);
  assert.match(script, /accelerators\?\.vulkan/);
  assert.match(script, /gpu\?\.vram_bytes \|\| report\.system_memory_bytes/);
  assert.doesNotMatch(script, /data-field="install-backend"/);
  assert.doesNotMatch(script, /model_url|runtime_url|sha256:/);
  assert.match(script, /\/api\/eval\/measure/);
});

test("Setup strip has settings and window controls but no Chat page button", () => {
  assert.match(html, /class="setup-settings"/);
  assert.match(html, /class="shell-window-controls"/);
  assert.doesNotMatch(html, />Chat<|setup-chat/);
});

test("Role assignment offers one click and per-slot b c d", () => {
  for (const role of ["b", "c", "d"]) assert.match(script, new RegExp(`data-role=\\"${role}\\"`));
  assert.match(script, /assignAll/);
  assert.match(script, /toolset: current\.toolset \|\| fullTools/);
});

test("Versioned recommendation table stays small and memory-class keyed", () => {
  assert.equal(recommendationTableVersion, "2026-09-07");
  assert.ok(recommendationTable.length <= 20);
  assert.equal(recommendationForBytes(8 * 1024 ** 3).model, "qwen3.5:4b");
  assert.equal(recommendationForBytes(32 * 1024 ** 3).model, "qwen3.5:27b");
  assert.equal(recommendationForBytes(128 * 1024 ** 3).model, "qwen3.5:122b");
});

test("Local detection is read-only and covers accelerators, servers, and interpreters", () => {
  for (const name of ["Ollama", "LM Studio", "llama-server", "python", "node", "go", "dotnet"]) assert.match(detector, new RegExp(`name='${name}'`));
  assert.doesNotMatch(detector, /\$env:Path\s*=|Start-Process|Invoke-WebRequest|Invoke-RestMethod/);
  assert.match(detector, /runs as service/);
  assert.match(detector, /operator-only/);
  assert.match(detector, /accelerators/);
  assert.match(detector, /vulkan-1\.dll/);
});

test("Setup follows the console palette and reduced motion is zero seconds", () => {
  assert.doesNotMatch(styles, /#[0-9a-f]{3,8}/i);
  assert.match(styles, /prefers-reduced-motion:reduce/);
  assert.match(styles, /transition-duration:0ms!important;animation-duration:0ms!important/);
});
