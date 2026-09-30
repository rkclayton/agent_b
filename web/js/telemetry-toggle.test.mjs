import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

// Item 2ny: SETTINGS SHOWS TELEMETRY AS ITS TOGGLE. The operator's words, 2026-09-28
// 23:58: "remove the what was sent and telemtry endpoint visibility in settings we just
// need the toggle thats it". Item 2ov later adds only the destination host to the disclaimer.
const about = readFileSync(new URL("./settings-about.js", import.meta.url), "utf8");

test("About carries the toggle and destination host without batch detail", () => {
  assert.match(about, /toggle\("telemetry\.enabled", "Send anonymous data to help improve Agent_b"/);
  assert.match(about, /class="settings-subhead-note"/);
  assert.match(about, /Only diagnostic data is sent — counts, durations and error classes\. Never your chats, files or prompts\./);
  assert.match(about, /Sent to \$\{html\(destination\)\}/);
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

test("About renders the default and configured destination hosts", async () => {
  const { renderAboutPage } = await import("./settings-about.js");
  const context = { store: { config: { telemetry: {} }, build: {}, signature: {} }, row: (_label, value) => value, toggle: () => "toggle", html: String, signInStart: {} };
  assert.match(renderAboutPage(context), /Sent to broker\.agentb\.app/);
  context.store.config.telemetry.endpoint = "https://collector.example.test:8443/intake";
  assert.match(renderAboutPage(context), /Sent to collector\.example\.test:8443/);
});

test("the unused telemetry styles go with the rows", () => {
  const css = readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
  for (const rule of ["settings-telemetry-list", "settings-telemetry-batch", "settings-telemetry-empty"]) {
    assert.doesNotMatch(css, new RegExp(rule), `app.css still styles .${rule}`);
  }
});
