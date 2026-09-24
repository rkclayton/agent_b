import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { renderAboutPage } from "./settings-about.js";

const chat = readFileSync(new URL("./chat.js", import.meta.url), "utf8");
const htmlSource = readFileSync(new URL("../index.html", import.meta.url), "utf8");

test("the update notice is a dismissible banner beside Attach, not strip text", () => {
  assert.match(htmlSource, /id="chat-update-banner"[\s\S]*id="chat-update-install"[\s\S]*id="chat-update-dismiss"[\s\S]*class="chat-attach-wrap"/);
  assert.match(chat, /`\$\{version\} available —`/);
  assert.match(chat, /finish or stop the run first/);
  assert.match(chat, /action: "install", session_id: session\.id/);
  assert.doesNotMatch(chat, /updateAvailableText/);
});

test("About exposes attach-aware timestamps, release note, and operator actions", () => {
  const html = renderAboutPage({
    store: { build: { tag: "v1.4.0", commit: "abcdef0123" }, server_started_at: "2026-09-23T12:00:00Z", config: { updates: { auto_check: true } }, update: { available: true, version: "v1.5.0", notes: "Distribution release", checked_at: "2026-09-23T12:05:00Z" } },
    row: (label, control) => `<div>${label}:${control}</div>`,
    toggle: (path, label, value, hint) => `<button data-path="${path}" data-value="${value}" title="${hint}">${label}</button>`,
    html: (value) => String(value),
  });
  assert.match(html, /updates\.auto_check/);
  assert.match(html, /api\.github\.com/);
  assert.match(html, /v1\.5\.0 available · Distribution release/);
	assert.match(html, /server started .*v1\.4\.0/);
	assert.match(html, /checked /);
	assert.match(html, /data-action="check-update"/);
  assert.match(html, /data-action="install-update"/);
});
