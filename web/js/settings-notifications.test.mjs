import assert from "node:assert/strict";
import test from "node:test";
import { renderNotificationsPage } from "./settings-notifications.js";

function render(status, message = "") {
	return renderNotificationsPage({
		notificationStatus: status,
		notificationBusy: false,
		notificationMessage: message,
		notificationAlarm: false,
		html: (value) => String(value),
		attr: (value) => String(value),
		store: { config: { notifications: { desktop_needs_you: true, desktop_finished: true, desktop_scheduled: true } } },
		row: (label, control) => `<div class="setting-row"><label>${label}</label><div>${control}</div></div>`,
		subhead: (label, hint) => `<div class="settings-subhead">${label}</div><p class="settings-subhead-note">${hint}</p>`,
	});
}

test("Notifications exposes desktop event switches and the existing webhook controls", () => {
	const html = render({ configured: true, host: "discord.com", desktop: true, phone: "ready" });
	assert.match(html, /Discord webhook URL/);
	assert.match(html, />Send test</);
	assert.match(html, /type="password"/);
	assert.equal((html.match(/role="switch"/g) || []).length, 4);
	assert.match(html, /data-path="notifications\.desktop"/);
	assert.match(html, /notifications\.desktop_needs_you/);
	assert.doesNotMatch(html, /modal|Agent A|email|Slack/i);
});

test("Notifications shows each truthful phone state and per-way answers", () => {
	for (const state of ["ready", "not paired", "paired, no notification permission"])
		assert.match(render({ phone: state }), new RegExp(state === "ready" ? "on · ready" : `off · ${state}`));
	const html = render({ phone: "not paired" }, "desktop · shown · webhook · accepted · phone · not paired");
	assert.match(html, /desktop · shown · webhook · accepted · phone · not paired/);
});
