import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

// Item 2ny: SETTINGS SHOWS TELEMETRY AS ITS TOGGLE. The operator's words, 2026-09-28
// 23:58: "remove the what was sent and telemtry endpoint visibility in settings we just
// need the toggle thats it". Item 2ov later adds only the destination host to the disclaimer.
const about = readFileSync(new URL("./settings-about.js", import.meta.url), "utf8");

test("About carries one concise toggle with the destination in its hover detail", () => {
  assert.match(about, /toggle\("telemetry\.enabled", "anonymous diagnostics"/);
  assert.match(about, /Sends counts, durations and error classes to \$\{destination\}; never chats, files or prompts\./);
  assert.doesNotMatch(about, /class="settings-subhead-note"/);
  for (const gone of ["what was sent", "telemetry_sent", "settings-telemetry-batch", "settings-telemetry-list", "settings-telemetry-empty", "no receiver configured"]) {
    assert.doesNotMatch(about, new RegExp(gone.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), `Settings still shows ${gone}`);
  }
});

test("nothing in the page reads sent batches", () => {
  const modules = ["settings.js", "settings-about.js", "settings-security.js", "settings-general.js", "bus.js", "shell.js"];
  for (const name of modules) {
    const source = readFileSync(new URL(`./${name}`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /telemetry_sent/, `${name} still reads telemetry_sent`);
  }
});

test("About passes the default and configured destination hosts to the toggle detail", async () => {
  const { renderAboutPage } = await import("./settings-about.js");
  const details = [];
  const context = { store: { config: { telemetry: {} }, build: {}, signature: {} }, row: (_label, value) => value, toggle: (_key, _label, _on, detail) => { details.push(detail); return "toggle"; }, html: String, signInStart: {} };
  renderAboutPage(context);
  assert.match(details.pop(), /to broker\.agentb\.app;/);
  context.store.config.telemetry.endpoint = "https://collector.example.test:8443/intake";
  renderAboutPage(context);
  assert.match(details.pop(), /to collector\.example\.test:8443;/);
});

test("the unused telemetry styles go with the rows", () => {
  const css = readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
  for (const rule of ["settings-telemetry-list", "settings-telemetry-batch", "settings-telemetry-empty"]) {
    assert.doesNotMatch(css, new RegExp(rule), `app.css still styles .${rule}`);
  }
});
