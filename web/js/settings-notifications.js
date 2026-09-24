let context;

export function renderNotificationsPage(pageContext) {
	context = pageContext;
	const { notificationStatus, notificationBusy, notificationMessage, notificationAlarm, row, subhead, html } = context;
	const configured = !!notificationStatus?.configured;
	const host = configured ? `Configured for ${notificationStatus.host || "Discord"}. Leave the field blank to keep it.` : "No Discord webhook configured.";
	return `${subhead("Discord", "Sends one message when a run needs you or finishes.")}
		${row("webhook URL", `<input id="discord-webhook-url" type="password" autocomplete="off" placeholder="${configured ? "•••• set" : "https://discord.com/api/webhooks/…"}" aria-label="Discord webhook URL" title="The URL is protected with Windows DPAPI and is never written to harness.json or logs.">`, notificationAlarm ? "invalid" : "")}
		<p class="settings-note">${html(host)}</p>
		${row("actions", `<div class="settings-actions">
			<button type="button" data-action="save-notification" ${notificationBusy ? "disabled" : ""}>Save URL</button>
			<button type="button" data-action="test-notification" ${notificationBusy || !configured ? "disabled" : ""}>Send test</button>
			<button type="button" data-action="clear-notification" ${notificationBusy || !configured ? "disabled" : ""}>Disable</button>
		</div>`)}
		${notificationMessage ? `<p class="settings-note ${notificationAlarm ? "alarm" : ""}">${html(notificationMessage)}</p>` : ""}`;
}
