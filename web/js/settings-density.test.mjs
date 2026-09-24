import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const modules = [
  "settings.js", "settings-security.js", "settings-connections.js", "settings-run.js",
  "settings-general.js", "settings-about.js", "settings-workspace.js",
  "settings-context.js", "settings-chats.js", "settings-notifications.js",
].map((name) => [name, readFileSync(new URL(`./${name}`, import.meta.url), "utf8")]);

const source = Object.fromEntries(modules);

test("subhead explanations are visible and row hints remain attached to controls", () => {
  // The helpers that carry it.
  assert.match(source["settings.js"], /function row\(label, control, extra = "", hint = ""\)/);
  assert.match(source["settings.js"], /function subhead\(label, hint = ""\)/);
  assert.match(source["settings.js"], /function field\(path, label, control, alarm = false, hint = ""\)/);
  for (const helper of ["toggle", "text", "number", "secret", "choices", "textarea", "approvalChoices"]) {
    assert.match(source["settings.js"], new RegExp(`function ${helper}\\([^)]*hint = ""\\)`), `${helper} cannot carry hover text`);
  }
  // Row-specific hints stay attached; subsection prose is visible and has no title.
  assert.match(source["settings.js"], /const hover = hint \|\| `\$\{label\} setting\.`/);
  assert.match(source["settings.js"], /class="setting-row \$\{extra\}" title="\$\{attr\(hover\)\}"/);
  assert.match(source["settings.js"], /class="settings-subhead">\$\{html\(label\)\}<\/div>\$\{hint \? `<p class="settings-subhead-note">/);
  assert.doesNotMatch(source["settings.js"], /class="settings-subhead"[^\n]+title=/);
});

test("the descriptor budget: Security keeps the dangerous line and the two authorized boundary explanations", () => {
  const security = source["settings-security.js"];
  const prose = [...security.matchAll(/<p class="settings-note">([\s\S]*?)<\/p>/g)].map((m) => m[1]);
	// Three deliberate lines, plus the "No active session." empty state, which is a
	// state readout rather than an explanation.
	const explanatory = prose.filter((line) => !/No active session/.test(line));
	assert.equal(explanatory.length, 3, `expected three visible explanations, got:\n${explanatory.join("\n")}`);
	assert.match(explanatory[0], /defeats the service-account OS boundary/);
	assert.match(explanatory[1], /tests the credential before enabling/);
	assert.match(explanatory[2], /loopback and the configured model server/);
});

test("no Settings section carries a paragraph a control could carry instead", () => {
  for (const [name, text] of modules) {
    if (name === "settings.js" || name === "settings-security.js") continue;
    const prose = [...text.matchAll(/<p class="settings-note">([\s\S]*?)<\/p>/g)].map((m) => m[1].replace(/<[^>]+>/g, "").trim());
    for (const line of prose) {
      // What is left must be a readout or an empty state, not an explanation:
      // it interpolates live data, or it says there is nothing to show.
      const readout = /\$\{/.test(line) || /^No [a-z]/.test(line) || /^none\b/i.test(line);
      assert.ok(readout, `${name} still shows explanatory prose: ${line.slice(0, 90)}`);
    }
  }
});

test("subnet chips share the switch's row and never orphan", () => {
  const security = source["settings-security.js"];
  // The chips are inside the row's control cell, not a block after it.
  assert.match(security, /data-action="local-network-toggle"><\/button><span class="settings-subnets"/);
  assert.match(security, /class="settings-chip"/);
  assert.doesNotMatch(security, /<div class="settings-subnets"/);
  const styles = readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
  const rule = styles.match(/\.settings-subnets \{([^}]+)\}/);
  assert.ok(rule, "no .settings-subnets rule");
  assert.match(rule[1], /display: inline-flex/);
  assert.match(rule[1], /flex-wrap: wrap/);
});

test("every control that carried a paragraph still exists", () => {
  // The inventory is compared before/after by scripts/settings-density-evidence.mjs;
  // this pins the controls whose prose moved, so a later edit cannot drop one.
  const security = source["settings-security.js"];
  for (const control of [
    'data-action="operator-context"',
    'toggle("sandbox.enabled", "Docker Sandbox"',
    'data-action="local-network-toggle"',
    'data-local-subnet',
  ]) {
    assert.ok(security.includes(control), `Security lost ${control}`);
  }
  assert.match(source["settings-run.js"], /approvalChoices\(cfg\.approval\?\.mode, "With the service identity enabled/);
  assert.match(source["settings-connections.js"], /probe_mode.{0,40}\["full", "minimal", "off"\], connection\.probe_mode, "minimal and off skip checks/);
  assert.doesNotMatch(source["settings.js"], /settings-delivery|renderDeliveryPage/);
});
