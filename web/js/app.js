import { api, setActive, store, subscribe } from "./bus.js";
import { renderTabs } from "./tabs.js";
import { renderRail } from "./rail.js";
import { renderFlow } from "./flow.js";
import { renderRack } from "./rack.js";
import { renderState } from "./state.js";
import { renderTimeline } from "./timeline.js";
import { initSettings } from "./settings.js";
import { createOperatorStatusController, isOperatorStateEvent } from "./operator-status.js";
import { createSessionResetController } from "./session-reset.js";
const consoleLaunch = document.getElementById("console-launch"),
  chatLaunch = document.getElementById("chat-launch"),
  operatorStatus = document.getElementById("operator-status"),
  clearConversation = document.getElementById("clear-conversation"),
  stop = document.getElementById("stop");
const requestedSession = new URLSearchParams(location.search).get("session");
let initialSession = requestedSession;
let renderFrame = 0;
const operatorControl = createOperatorStatusController(operatorStatus, {
  identity: () => store.shell_identity,
  interactive: () => !store.replay,
  setOperatorContext: (enabled) => api("/api/config", { shell: { operator_context: enabled } }),
  reportError: showError,
});
const resetControl = createSessionResetController(clearConversation, {
  session: () => store.sessions[store.active],
  interactive: () => !store.replay,
  confirmClear: (message) => window.confirm(message),
  reset: (id, force) => api(`/api/sessions/${encodeURIComponent(id)}/reset${force ? "?force=1" : ""}`, {}),
  reportError: showError,
});
initSettings();
subscribe((_state, event) => {
  if (isOperatorStateEvent(event)) operatorControl.render();
  if (event.type === "snapshot" && initialSession && store.sessions[initialSession]) {
    const id = initialSession;
    initialSession = "";
    setActive(id);
    return;
  }
  // Incremental text projection changes only the live Activity readout on Console.
  // Chat consumes the same patch independently; avoid rebuilding History/State.
  if (event.type === "projection.patch" && (event.data?.operations || []).some((operation) =>
    operation.path === "/run/partial" || /^\/chat\/[^/]+\/(reasoning|text)$/.test(operation.path))) {
    scheduleFlowRender();
    return;
  }
  scheduleRender();
});

function scheduleRender() {
  if (renderFrame) return;
  renderFrame = requestAnimationFrame(renderConsole);
}
let flowFrame = 0;
function scheduleFlowRender() {
  if (flowFrame) return;
  flowFrame = requestAnimationFrame(() => {
    flowFrame = 0;
    renderFlow();
  });
}
setInterval(() => {
  const session = store.sessions[store.active];
  if (session && session.run.status === "running" && session.activity?.stage === "call_model")
    scheduleFlowRender();
}, 1000);

function renderConsole() {
  renderFrame = 0;
  renderTabs();
  renderRail();
  renderFlow();
  renderRack();
  renderState();
  renderTimeline();
	resetControl.render();
	const identityAlarm = document.getElementById("shell-identity-alarm");
	const identityUnavailable = store.shell_identity?.operator_approval_required || store.shell_identity?.fallback;
	operatorControl.render();
	identityAlarm.hidden = !identityUnavailable;
	identityAlarm.textContent = identityUnavailable
		? `Service identity unavailable · shell requires operator approval · ${store.shell_identity.reason}`
		: "";
  const s = store.sessions[store.active];
  const query = new URLSearchParams();
  if (s) query.set("session", s.id);
  const suffix = query.size ? `?${query}` : "";
  consoleLaunch.href = `/${suffix}`;
	chatLaunch.href = `/chat${suffix}`;
	stop.hidden = !!store.replay;
	document.getElementById("mode").textContent = store.replay ? "replay" : "";
	const build = store.build || {};
	const buildID = document.getElementById("build-id");
	buildID.textContent = `build ${build.display || "unknown"}`;
	buildID.title = build.known ? `Build ${build.commit}${build.dirty ? " (dirty worktree)" : " (clean commit)"}` : "Build identity unavailable";
}
stop.onclick = (event) => {
  const s = store.sessions[store.active];
  if (s)
    api(
      "/api/stop",
      event.shiftKey ? { all: true } : { session_id: s.id },
    ).catch((error) => showError(error.message));
};
document.addEventListener("keydown", (event) => {
  if (event.ctrlKey && /^[1-9]$/.test(event.key)) {
    const id = Object.keys(store.sessions)[Number(event.key) - 1];
    if (id) {
      event.preventDefault();
      setActive(id);
    }
  }
});
function showError(message) {
  const node = document.getElementById("connection");
  node.textContent = message;
  node.className = "alarm";
}
