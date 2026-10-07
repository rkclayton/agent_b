import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

const workflow = fs.readFileSync(new URL("../.github/workflows/ci.yml", import.meta.url), "utf8");

test("CI references no secret and never installs, signs, or contacts operator services", () => {
  assert.doesNotMatch(workflow, /secrets\s*\.|install-Agent_b|deploy-release|sign-release|acme/i);
  assert.match(workflow, /windows-latest/);
  assert.match(workflow, /ubuntu-latest/);
  assert.match(workflow, /go test -race \.\/internal\/events/);
  assert.match(workflow, /node tests\/run-node-tests\.mjs/);
  assert.equal((workflow.match(/privacy-gate\.mjs --general-only/g) ?? []).length, 2);
});
