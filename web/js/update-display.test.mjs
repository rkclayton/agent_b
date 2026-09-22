import test from "node:test";
import assert from "node:assert/strict";
import { updateAvailableText } from "./update-display.js";
import { renderAboutPage } from "./settings-about.js";

test("the strip reports only an available version", () => {
  assert.equal(updateAvailableText({ available: true, version: "v1.5.0" }), "v1.5.0 available");
  assert.equal(updateAvailableText({ available: false, version: "v1.5.0" }), "");
});

test("About exposes the default-on switch, release note, and Update action", () => {
  const html = renderAboutPage({
    store: { build: { tag: "v1.4.0", commit: "abcdef0123" }, config: { updates: { auto_check: true } }, update: { available: true, version: "v1.5.0", notes: "Distribution release" } },
    row: (label, control) => `<div>${label}:${control}</div>`,
    toggle: (path, label, value, hint) => `<button data-path="${path}" data-value="${value}" title="${hint}">${label}</button>`,
    html: (value) => String(value),
  });
  assert.match(html, /updates\.auto_check/);
  assert.match(html, /api\.github\.com/);
  assert.match(html, /v1\.5\.0 available · Distribution release/);
  assert.match(html, /data-action="install-update"/);
});
