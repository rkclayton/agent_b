let installed = false;
export const UI_ERROR_DEDUPE_MS = 1000;
export const UI_ERROR_REPEAT_CAP = 25;

export function installUIErrorRelay(context = {}) {
	if (installed || typeof window === "undefined") return;
	installed = true;
	const original = console.error.bind(console);
	const errors = new Map();
	const post = (kind, message, stack, repeatCount, capped, origin) => {
		try {
			const token = context.token?.() || "";
			fetch("/api/ui-errors", {
				method: "POST",
				headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": token },
				body: JSON.stringify({ session_id: context.sessionID?.() || "", kind, message, stack, location: window.location.href, repeat_count: repeatCount, capped, ...origin }),
				keepalive: true,
			}).catch(() => {});
		} catch {}
	};
	const relay = (kind, value, stackValue = "", origin = {}) => {
		const message = String(value || "");
		const stack = String(stackValue || "");
		const fingerprint = `${kind}\n${message}\n${stack}`;
		let state = errors.get(fingerprint);
		if (!state) {
			state = { total: 1, reported: 1, timer: 0, capped: false };
			errors.set(fingerprint, state);
			post(kind, message, stack, 1, false, origin);
			return;
		}
		if (state.capped) return;
		state.total++;
		if (state.total >= UI_ERROR_REPEAT_CAP) {
			if (state.timer) clearTimeout(state.timer);
			state.timer = 0;
			state.capped = true;
			post(kind, message, stack, state.total, true, origin);
			return;
		}
		if (state.timer) clearTimeout(state.timer);
		state.timer = setTimeout(() => {
			state.timer = 0;
			if (state.capped || state.reported === state.total) return;
			state.reported = state.total;
			post(kind, message, stack, state.total, false, origin);
		}, UI_ERROR_DEDUPE_MS);
	};
	console.error = (...values) => {
		original(...values);
		relay("console.error", values.map(stringify).join(" "));
	};
	window.addEventListener("error", (event) => relay("unhandled exception", event.message, event.error?.stack || "", errorOrigin(event.error, event.filename, event.lineno)));
	window.addEventListener("unhandledrejection", (event) => relay("unhandled rejection", stringify(event.reason), event.reason?.stack || "", errorOrigin(event.reason)));
}

// Item 2q7 (b): an error's name and where in OUR code it was thrown — a file
// name of ours under /js/ and a line number, never a message or a value.
export function errorOrigin(error, filename = "", line = 0) {
	const name = typeof error?.name === "string" && /^[A-Za-z]{1,40}$/.test(error.name) ? error.name : "";
	let file = filename;
	let at = line;
	if (!file && typeof error?.stack === "string") {
		const frame = /\/js\/([a-z0-9-]+\.m?js):(\d+)/.exec(error.stack);
		if (frame) [file, at] = [frame[1], Number(frame[2])];
	}
	const ours = /\/js\/([a-z0-9-]+\.m?js)$/.exec(String(file).split("?")[0]) || /^([a-z0-9-]+\.m?js)$/.exec(String(file));
	return ours ? { name, file: ours[1], line: Number(at) || 0 } : { name };
}

function stringify(value) {
	if (value instanceof Error) return value.stack || value.message;
	if (typeof value === "string") return value;
	try { return JSON.stringify(value); } catch { return String(value); }
}
