import assert from "node:assert/strict";
import test from "node:test";
import { renderNotificationsPage } from "./settings-notifications.js";

test("Notifications exposes only the requested URL and test controls", () => {
	const html = renderNotificationsPage({
		notificationStatus: { configured: true, host: "discord.com" },
		notificationBusy: false,
		notificationMessage: "",
		notificationAlarm: false,
		html: (value) => String(value),
		attr: (value) => String(value),
		row: (label, control) => `<div class="setting-row"><label>${label}</label><div>${control}</div></div>`,
		subhead: (label, hint) => `<div class="settings-subhead">${label}</div><p class="settings-subhead-note">${hint}</p>`,
	});
	assert.match(html, /Discord webhook URL/);
	assert.match(html, />Send test</);
	assert.match(html, /type="password"/);
	assert.doesNotMatch(html, /modal|Agent A|email|Slack/i);
});
