// Item 2q7 (b) and (f): what only the page can see — its longest freeze and
// which settings pages were opened — told to the app in one small post every
// ten minutes and when the page is hidden. Counts and a number; no text.

export const PAGE_HEALTH_EVERY_MS = 10 * 60 * 1000;

let freezeMS = 0;
let pages = {};
let installed = false;

// notePage counts one settings page opened, by its section id.
export function notePage(id) {
	if (typeof id === "string" && /^[a-z]{1,24}$/.test(id)) pages[id] = (pages[id] || 0) + 1;
}

export function installPageHealth(context = {}, runtime = globalThis) {
	if (installed) return;
	installed = true;
	try {
		new runtime.PerformanceObserver((list) => {
			for (const entry of list.getEntries()) freezeMS = Math.max(freezeMS, Math.round(entry.duration));
		}).observe({ type: "longtask", buffered: true });
	} catch {}
	const send = () => {
		if (!freezeMS && !Object.keys(pages).length) return;
		const body = JSON.stringify({ freeze_ms: freezeMS, settings_pages: pages });
		freezeMS = 0;
		pages = {};
		try {
			runtime.fetch("/api/page-health", { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": context.token?.() || "" }, body, keepalive: true }).catch(() => {});
		} catch {}
	};
	runtime.setInterval(send, PAGE_HEALTH_EVERY_MS);
	runtime.addEventListener?.("pagehide", send);
	return send;
}
