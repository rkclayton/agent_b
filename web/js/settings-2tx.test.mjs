import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { renderAboutPage } from "./settings-about.js";
import { renderNotificationsPage } from "./settings-notifications.js";

const row = (label, control, extra = "", hint = "") => `<div class="setting-row ${extra}" title="${hint}"><label>${label}</label><div>${control}</div></div>`;
const subhead = (label, hint = "") => `<div class="settings-subhead" title="${hint}">${label}</div>`;

test("Settings panel uses four fifths and rows start values beside labels 2tx", () => {
  const css = fs.readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
  assert.match(css, /\.settings-page\s*\{[^}]*width:\s*80vw;[^}]*height:\s*80dvh;/s);
  assert.match(css, /\.setting-row\s*>\s*div\s*\{[^}]*justify-content:\s*flex-start;/s);
  assert.match(css, /@media \(max-width:\s*639px\)[\s\S]*?\.settings-page\s*\{[^}]*inset:/);
});

test("About renders anonymous diagnostics as an ordinary labelled row 2tx", () => {
  const context = {
    store: { build: {}, signature: {}, update: {}, config: { telemetry: {}, updates: {} } },
    row,
    toggle: (_path, label, _on, hint) => row(label, '<button role="switch"></button>', "", hint),
    html: String,
    signInStart: { loaded: true },
  };
  const page = renderAboutPage(context);
  assert.match(page, /<label>anonymous diagnostics<\/label>/);
  assert.doesNotMatch(page, /Only diagnostic data is sent/);
  assert.match(page, /title="[^"]*counts, durations and error classes/);
});

test("Notifications separates desktop phone and Discord and exposes each desktop event group 2tx", () => {
  const page = renderNotificationsPage({
    notificationStatus: { configured: true, desktop: true, phone: "ready" },
    notificationBusy: false,
    notificationMessage: "",
    notificationAlarm: false,
    row,
    subhead,
    html: String,
    attr: String,
  });
  for (const heading of ["Windows desktop", "Phone", "Discord"])
    assert.match(page, new RegExp(`settings-subhead[^>]*>${heading}<`));
  for (const path of ["notifications.desktop_needs_you", "notifications.desktop_finished", "notifications.desktop_scheduled"])
    assert.match(page, new RegExp(path.replace(".", "\\.")));
  for (const way of ["desktop", "phone", "discord"])
    assert.match(page, new RegExp(`data-action="test-notification" data-id="${way}"`));
});
