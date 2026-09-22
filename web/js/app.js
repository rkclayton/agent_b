import { api, reduce, setActive, store, subscribe } from "./bus.js";
import { renderRail } from "./rail.js";
import { renderFlow } from "./flow.js";
import { renderRack } from "./rack.js";
import { renderState } from "./state.js";
import { renderTimeline } from "./timeline.js";
import { createMessageDropController } from "./message-drop.js";
import { createApprovalCard } from "./approval.js";
import { loadReflection } from "./reflection.js";
import { agentKey, compactionFigures, lifetimeRows, ratio } from "./panel-lifetime.js";
import { renderStopState } from "./stop-state.js";
import { navigationSurfaceReady } from "./navigation-telemetry.js";
import { liveActivityText } from "./chat-activity.js";

const requestedSession = new URLSearchParams(location.search).get("session");
let initialSession = requestedSession;
let selectedAgent = "";
let ledger = null;
// Item 2ew: "No lifetime activity" is said only once the ledger has been asked.
let ledgerAsked = false;
let renderFrame = 0;
let mounted = false;
const liveContent = document.getElementById("panel-live-content");
const liveEmpty = document.getElementById("panel-live-empty");
const dropLastMessage = document.getElementById("drop-last-message");
const agentSelect = document.getElementById("panel-agent");
const agentServerSelect = document.getElementById("panel-agent-server");
const agentServerVision = document.getElementById("panel-agent-vision");
const agentServerState = document.getElementById("panel-agent-server-state");
const agentServerCancel = document.getElementById("panel-agent-server-cancel");
const feedback = document.getElementById("panel-feedback");
const panelStop = document.getElementById("panel-stop");
const panelLiveCompactions = document.getElementById("panel-live-compactions");
const panelRunResult = document.getElementById("panel-run-result");
const panelRunStop = document.getElementById("panel-run-stop");
const panelRunLabel = document.getElementById("panel-run-label");

const dropControl = createMessageDropController(dropLastMessage, {
  session: () => store.sessions[store.active], interactive: () => !store.replay,
  confirmDrop: (message) => window.confirm(message),
  drop: (id) => api(`/api/sessions/${encodeURIComponent(id)}/messages/drop-last`, {}), reportError: showError,
});

agentSelect.addEventListener("change", () => {
  selectedAgent = agentSelect.value;
  const current = store.sessions[store.active];
  if (current?.agent_id !== selectedAgent) {
    const next = Object.values(store.sessions).filter((session) => !session.closed && session.agent_id === selectedAgent)
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0))[0];
    if (next) setActive(next.id);
  }
  void refreshLedger();
});
agentServerSelect.addEventListener("change", () => void changeAgentServer());
agentServerCancel.addEventListener("click", () => void cancelAgentServerChange());
document.getElementById("clear-stats").addEventListener("click", () => void clearStats());
document.getElementById("flush-memory").addEventListener("click", () => void flushMemory());
document.getElementById("panel-tools").addEventListener("change", (event) => void toggleTool(event));
document.getElementById("panel-tools-link").addEventListener("click", (event) => { event.preventDefault(); document.getElementById("panel-tools-panel").scrollIntoView({block:"start"}); });
panelStop.addEventListener("click", () => { const id=store.selection.session_id; if(id&&!store.replay) void api("/api/stop",{session_id:id}); });
panelRunLabel.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-label]");
  const id = store.selection.session_id;
  const runID = store.sessions[id]?.run?.last_run_id;
  if (button && id && runID && !store.replay) void api(`/api/sessions/${encodeURIComponent(id)}/result-label`, { label: button.dataset.label, run_id: runID }).catch((error) => showError(error.message));
});

subscribe((_state, event) => {
  if (event.type === "snapshot" && initialSession && store.sessions[initialSession]) {
    const id = initialSession; initialSession = ""; setActive(id);
    // Item 2fu: on a direct load this is the first snapshot; the ledger is asked
    // for here too, or lifetime and the tool counts stay empty until a redraw.
    if (mounted) { selectedAgent = store.sessions[id]?.agent_id || agentKey(store.config.agents?.[0]); void refreshLedger(false); }
    return;
  }
  if (!selectedAgent || ["selection.changed", "active.changed"].includes(event.type)) selectedAgent = store.sessions[store.active]?.agent_id || agentKey(store.config.agents?.[0]);
  if (event.type === "projection.patch" && (event.data?.operations || []).some((operation) => operation.path === "/run/partial" || /^\/chat\/[^/]+\/(reasoning|text)$/.test(operation.path))) {
    if (!liveContent.hidden) scheduleFlowRender();
    return;
  }
  if (mounted && (["snapshot", "config.changed"].includes(event.type) || (event.type === "projection.patch" && patchEndedRun(event.data)))) void refreshLedger(false);
  // Item 17-i: the reflection section is read-only text; it is fetched on the
  // first snapshot and again after a run ends, when a new summary may exist.
  if (mounted && (event.type === "snapshot" || (event.type === "projection.patch" && patchEndedRun(event.data)))) void loadReflection();
  scheduleRender();
});

function scheduleRender() {
  if (!mounted) return;
  if (!renderFrame) renderFrame = requestAnimationFrame(renderPanels);
}
let flowFrame = 0;
function scheduleFlowRender() {
  if (!mounted) return;
  if (flowFrame) return;
  flowFrame = requestAnimationFrame(() => { flowFrame = 0; renderFlow(); });
}
setInterval(() => {
  if (!mounted) return;
  const session = store.sessions[store.active];
  if (!liveContent.hidden && session?.run?.status === "running" && session.activity?.stage === "call_model") scheduleFlowRender();
}, 1000);

async function refreshLedger(render = true) {
  // Only a settled answer may say "no activity": the true empty cases (no agent, replay)
  // once the snapshot is in, or a completed fetch for the agent now selected.
  if (!selectedAgent || store.replay) { ledger = null; ledgerAsked = store.loaded; if (render) scheduleRender(); return; }
  const askedFor = selectedAgent;
  ledgerAsked = false;
  try { ledger = await api(`/api/stats/${encodeURIComponent(askedFor)}`, undefined, "GET"); ledgerAsked = askedFor === selectedAgent; }
  catch (error) { showError(error.message); }
  // Item 2fl: the answer is drawn when it arrives. The render the caller asked
  // for ran before it did, so skipping this left lifetime blank until a redraw.
  scheduleRender();
}

function renderPanels() {
  renderFrame = 0;
  if (!mounted) return;
  const agents = store.config.agents || [];
  if (!agents.some((agent) => agentKey(agent) === selectedAgent)) selectedAgent = agentKey(agents[0]);
  agentSelect.replaceChildren(...agents.map((agent) => option(agentKey(agent), agent.name, agentKey(agent) === selectedAgent)));
  const agent = agents.find((candidate) => agentKey(candidate) === selectedAgent);
  renderAgentServer(agent);
  document.getElementById("panel-agent-binding").textContent = agent ? `${agent.c ? `c ${agent.c}` : ""}${agent.c && agent.d ? " · " : ""}${agent.d ? `d ${agent.d}` : ""}` : store.loaded ? "No configured agents" : "";
  renderTools(agent);
  renderLifetime();
  const session = store.sessions[store.active];
  const hasSelectedChat = !!session && session.agent_id === selectedAgent;
  liveContent.hidden = !hasSelectedChat;
  liveEmpty.hidden = hasSelectedChat || !store.loaded;
  renderStopState(panelStop, hasSelectedChat ? session : null, store.replay);
  panelLiveCompactions.textContent = hasSelectedChat ? compactionFigures(session) : "";
  panelLiveCompactions.hidden = !hasSelectedChat;
  document.getElementById("panel-live-state").textContent = !store.loaded ? "" : !hasSelectedChat ? "no open chat" : session.pending_approval ? "waiting for you" : liveActivityText(session) || session.run?.status || "idle";
  renderRunResult(hasSelectedChat ? session : null);
  if (hasSelectedChat) {
    renderRail(); renderFlow(); renderRack(); renderState(); renderTimeline(); placeDropLastMessage(); dropControl.render(); renderPendingApproval(session);
  }
  navigationSurfaceReady("panels", store);
}

function renderRunResult(session) {
  const run = session?.run;
  const ended = !!run?.last_stop_reason && !["running", "queued", "stopping"].includes(run.status);
  panelRunResult.hidden = !ended;
  if (!ended) { panelRunStop.textContent = ""; panelRunLabel.replaceChildren(); return; }
  const armed = run.armed_detectors?.length ? run.armed_detectors.join(", ") : "none";
  panelRunStop.textContent = `Ended: ${run.last_stop_reason}${run.last_stop_detail ? ` — ${run.last_stop_detail}` : ""} · armed: ${armed}`;
  panelRunLabel.replaceChildren(...["productive", "stuck", "mixed"].map((label) => {
    const button = node("button", run.result_label === label ? "selected" : "");
    button.type = "button"; button.dataset.label = label; button.textContent = label; button.disabled = store.replay;
    return button;
  }));
}

export function mountPanels() {
  mounted = true;
  void refreshLedger(false);
  scheduleRender();
}

export function unmountPanels() {
  mounted = false;
  if (renderFrame) cancelAnimationFrame(renderFrame);
  if (flowFrame) cancelAnimationFrame(flowFrame);
  renderFrame = 0;
  flowFrame = 0;
}

function renderAgentServer(agent) {
  const profiles = store.servers?.length ? store.servers : store.config.servers || [];
  const pending = store.agent_server_changes?.[selectedAgent];
  agentServerSelect.replaceChildren(...profiles.map((profile) => option(profile.id, profile.label || profile.id, profile.id === agent?.b)));
  agentServerSelect.disabled = !agent || store.replay;
  const profile = profiles.find((candidate) => candidate.id === agent?.b);
  const vision = profile?.capabilities?.vision || "not classified";
  const visionFinding = (profile?.capabilities?.findings || []).find((finding) => finding.startsWith("vision:"));
  agentServerVision.hidden = !profile?.capabilities?.probed_at;
  agentServerVision.className = `panel-agent-vision ${vision === "reads images" ? "reads" : "does-not-read"}`;
  agentServerVision.title = visionFinding || `vision: ${vision}`;
  agentServerVision.setAttribute("aria-label", agentServerVision.title);
  agentServerState.textContent = !agent ? "" : pending ? `Applied ${agent.b} · pending ${pending.to}` : `Applied ${agent.b}`;
  agentServerState.className = pending ? "pending" : "";
  agentServerCancel.hidden = !pending;
  agentServerCancel.disabled = store.replay;
}

async function changeAgentServer() {
  if (!selectedAgent || store.replay) return;
  const serverID = agentServerSelect.value;
  try {
    const result = await api(`/api/agents/${encodeURIComponent(selectedAgent)}/server`, { action: "set", server_id: serverID });
    showFeedback(result.status === "pending" ? `Server change queued for ${selectedAgent}.` : `Server changed for ${selectedAgent}.`);
    await refreshState();
  } catch (error) { showError(error.message); scheduleRender(); }
}

async function cancelAgentServerChange() {
  if (!selectedAgent || store.replay) return;
  try {
    await api(`/api/agents/${encodeURIComponent(selectedAgent)}/server`, { action: "cancel" });
    showFeedback(`Pending server change cancelled for ${selectedAgent}.`);
    await refreshState();
  } catch (error) { showError(error.message); }
}

// Item 2gk: one table became two, because the two halves answer to different
// people. Turning a tool on or off is setting the agent up, so it is on Agents;
// how often a tool ran and how often it failed is the record of what happened,
// so it is on Activity. Every column that was in the one table is still drawn,
// and both halves read the one ledger.
function renderTools(agent) {
  const root = document.getElementById("panel-tools");
  const counts = document.getElementById("panel-tool-counters");
  if (!agent) {
    const empty = store.loaded ? '<p class="panel-empty">No agent selected.</p>' : "";
    root.innerHTML = empty;
    counts.innerHTML = empty;
    return;
  }
	const enabled = new Set(agent.toolset || []);
	// web_search is configured only in harness.json for this release. Keep it
	// observable in Activity, but do not invent a Settings control for it.
	const configurable = (store.tools || []).filter((tool) => tool.name !== "web_search");
	const configurableEnabled = configurable.filter((tool) => enabled.has(tool.name));
	document.getElementById("panel-tools-link").textContent = `${configurableEnabled.length} tools active`;
	const counters = ledger?.agent?.tools || {};
	root.replaceChildren(...configurable.map((tool) => {
    const row = node("label", "panel-line panel-tool-line");
    const toggle = document.createElement("input");
    toggle.type = "checkbox"; toggle.checked = enabled.has(tool.name); toggle.dataset.tool = tool.name; toggle.disabled = store.replay;
    row.append(toggle, text(tool.name));
    return row;
  }));
  counts.replaceChildren(...(store.tools || []).map((tool) => {
    const stats = counters[tool.name] || {};
    const row = node("div", "panel-line panel-tool-line panel-tool-counts");
    row.append(text(tool.name), text(stats.calls || 0), text(ratio(stats.failures || 0, stats.calls || 0)), text(stats.last_used || "—"));
    return row;
  }));
}

function renderLifetime() {
  const root = document.getElementById("panel-stats");
  if (!ledger) { root.innerHTML = store.loaded && ledgerAsked ? '<p class="panel-empty">No lifetime activity.</p>' : ""; return; }
  const sections = [[selectedAgent, ledger.agent], ...Object.entries(ledger.profiles || {})];
  root.replaceChildren(...sections.flatMap(([name, counters]) => [text(name, "panel-profile-head"), ...lifetimeRows(counters, percentile).map(([label, value]) => line(label, value))]));
}

async function toggleTool(event) {
  const name = event.target?.dataset?.tool;
  if (!name || !selectedAgent) return;
  try { await api(`/api/tools/${encodeURIComponent(name)}`, { agent_id: selectedAgent, enabled: event.target.checked }); await refreshState(); }
  catch (error) { event.target.checked = !event.target.checked; showError(error.message); }
}

async function clearStats() {
  if (!selectedAgent || store.replay || !window.confirm(`Clear all lifetime stats for ${selectedAgent}?`)) return;
  try { ledger = await api(`/api/stats/${encodeURIComponent(selectedAgent)}/clear`, { confirm: true }); showFeedback(`Stats cleared for ${selectedAgent}.`); scheduleRender(); }
  catch (error) { showError(error.message); }
}

async function flushMemory() {
  const session = Object.values(store.sessions).find((item) => item.agent_id === selectedAgent && !item.closed) || store.sessions[store.active];
  if (!session || store.replay) return showError("An open chat is required to identify the folder.");
  try {
    const preview = await api(`/api/agents/${encodeURIComponent(selectedAgent)}/memory/flush`, { workspace: session.workspace, confirm: false });
    if (!window.confirm(`Flush memory for ${selectedAgent} and ${preview.workspace}?\n\n${preview.agent_entries} agent entries and ${preview.workspace_entries} folder entries will be removed.`)) return;
    await api(`/api/agents/${encodeURIComponent(selectedAgent)}/memory/flush`, { workspace: session.workspace, confirm: true });
    showFeedback(`Memory flushed for ${selectedAgent} and ${preview.workspace}.`); await refreshState();
  } catch (error) { showError(error.message); }
}

async function refreshState() { reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); await refreshLedger(); }
function placeDropLastMessage() { const heads = document.querySelectorAll(".timeline-model > .timeline-head"); const target = heads[heads.length - 1]; dropLastMessage.hidden = !target; if (target) target.append(dropLastMessage); }
function renderPendingApproval(session) { const root = document.getElementById("panel-pending-approval"); root.hidden = !session?.pending_approval; root.replaceChildren(...(session?.pending_approval ? [createApprovalCard(document, session.pending_approval, { replay: store.replay, decide: (callID, decision) => api("/api/approve", { session_id: session.id, call_id: callID, decision }) })] : [])); }
function percentile(values = [], fraction = .5) { if (!values.length) return 0; const copy = [...values].sort((a, b) => a - b); return copy[Math.floor((copy.length - 1) * fraction)]; }
function patchEndedRun(patch = {}) { return (patch.operations || []).some((operation) => operation.path === "/run" && ["idle", "held"].includes(operation.value?.status)); }
function line(label, value) { const row = node("div", "panel-line"); row.append(text(label), text(String(value))); return row; }
function node(tag, className = "") { const value = document.createElement(tag); value.className = className; return value; }
function text(value, className = "") { const result = node("span", className); result.textContent = String(value); return result; }
function button(value, title) { const result = node("button"); result.type = "button"; result.textContent = value; result.title = title; return result; }
function option(value, label, selected) { const result = document.createElement("option"); result.value = value; result.textContent = label; result.selected = selected; return result; }
function showFeedback(message) { feedback.textContent = message; feedback.className = ""; }
function showError(message) { feedback.textContent = message; feedback.className = "alarm"; const connection = document.getElementById("connection"); if (connection) { connection.textContent = message; connection.className = "alarm"; } }
