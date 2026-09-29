import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

// Item 2ny: SETTINGS SHOWS TELEMETRY AS ITS TOGGLE. The operator's words, 2026-09-28
// 23:58: "remove the what was sent and telemtry endpoint visibility in settings we just
// need the toggle thats it". The receiver state and the last-20-batches list go; what the
// toggle does is untouched.
const about = readFileSync(new URL("./settings-about.js", import.meta.url), "utf8");

test("About carries the toggle and nothing else about telemetry", () => {
  assert.match(about, /toggle\("telemetry\.enabled", "send diagnostic telemetry"/);
  assert.match(about, /Counts, durations and error classes only/);
  for (const gone of ["what was sent", "telemetry_sent", "settings-telemetry-batch", "settings-telemetry-list", "settings-telemetry-empty", "config.endpoint", "no receiver configured"]) {
    assert.doesNotMatch(about, new RegExp(gone.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), `Settings still shows ${gone}`);
  }
});

test("nothing in the page reads the sent batches or the endpoint", () => {
  const modules = ["settings.js", "settings-about.js", "settings-security.js", "settings-general.js", "bus.js", "shell.js"];
  for (const name of modules) {
    const source = readFileSync(new URL(`./${name}`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /telemetry_sent/, `${name} still reads telemetry_sent`);
    assert.doesNotMatch(source, /telemetry\?\.endpoint|telemetry\.endpoint/, `${name} still reads the telemetry endpoint`);
  }
});

test("the unused telemetry styles go with the rows", () => {
  const css = readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
  for (const rule of ["settings-telemetry-list", "settings-telemetry-batch", "settings-telemetry-empty"]) {
    assert.doesNotMatch(css, new RegExp(rule), `app.css still styles .${rule}`);
  }
});
