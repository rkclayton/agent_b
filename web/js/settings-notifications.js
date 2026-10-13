let context;

export function renderNotificationsPage(pageContext) {
	context = pageContext;
	const { notificationStatus, notificationBusy, notificationMessage, notificationAlarm, row, subhead, html, store = { config: {} } } = context;
	const configured = !!notificationStatus?.configured;
	const phone = notificationStatus?.phone || "not paired";
	const config = store.config?.notifications || {};
	const switchFor = (path, on) => `<button type="button" class="switch ${on ? "on" : ""}" role="switch" aria-checked="${on ? "true" : "false"}" data-path="${path}"><span></span></button>`;
	return `${subhead("Windows desktop", "Choose which desktop notices Agent_b may raise.")}
		${row("on", switchFor("notifications.desktop", !!notificationStatus?.desktop))}
		${row("needs you", switchFor("notifications.desktop_needs_you", config.desktop_needs_you !== false))}
		${row("finished", switchFor("notifications.desktop_finished", config.desktop_finished !== false))}
		${row("scheduled results", switchFor("notifications.desktop_scheduled", config.desktop_scheduled !== false))}
		${row("test", `<button type="button" data-action="test-notification" data-id="desktop" ${notificationBusy ? "disabled" : ""}>Send test</button>`)}
		${subhead("Phone", "Phone notification availability is controlled on the paired phone.")}
		${row("state", `<span>${html(phone === "ready" ? "on · ready" : `off · ${phone}`)}</span>`)}
		${row("test", `<button type="button" data-action="test-notification" data-id="phone" ${notificationBusy ? "disabled" : ""}>Send test</button>`)}
		${subhead("Discord", "Sends one message when a run needs you or finishes.")}
		${row("webhook URL", `<input id="discord-webhook-url" type="password" autocomplete="off" placeholder="${configured ? "•••• set" : "https://discord.com/api/webhooks/…"}" aria-label="Discord webhook URL" title="The URL is protected with Windows DPAPI and is never written to harness.json or logs.">`, notificationAlarm ? "invalid" : "")}
		${row("actions", `<div class="settings-actions notification-actions">
			<button type="button" data-action="save-notification" ${notificationBusy ? "disabled" : ""}>Save URL</button>
			<button type="button" data-action="test-notification" data-id="discord" ${notificationBusy ? "disabled" : ""}>Send test</button>
			<button type="button" data-action="clear-notification" ${notificationBusy || !configured ? "disabled" : ""}>Disable</button>
		</div>`)}
		${notificationMessage ? `<p class="settings-note ${notificationAlarm ? "alarm" : ""}">${html(notificationMessage)}</p>` : ""}`;
}
import { actionTable } from "./settings-action-table.js";

export const notificationActions = actionTable([
  "save-notification", "test-notification", "clear-notification",
]);
