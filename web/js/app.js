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
const liveIdle = document.getElementById("panel-live-idle");
const dropLastMessage = document.getElementById("drop-last-message");
const agentSelect = document.getElementById("panel-agent");
const roleTable = document.getElementById("panel-roles");
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
// Item 2iq (a): b's row and the strip's switcher write the same setting, so
// the table delegates rather than binding a listener per render.
roleTable.addEventListener("change", (event) => {
  if (event.target instanceof HTMLSelectElement && event.target.dataset.role === "b") void changeAgentConnection(event.target.value);
});
roleTable.addEventListener("click", (event) => {
  if (event.target instanceof HTMLElement && event.target.dataset.action === "cancel-pending") void cancelAgentConnectionChange();
});
document.getElementById("clear-stats").addEventListener("click", () => void clearStats());
document.getElementById("flush-memory").addEventListener("click", () => void flushMemory());
document.getElementById("panel-tools").addEventListener("change", (event) => void toggleTool(event));
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
  renderAgentConnection(agent);
  document.getElementById("panel-agent-binding").textContent = agent ? "" : store.loaded ? "No configured agents" : "";
  renderTools(agent);
  renderLifetime();
  const session = store.sessions[store.active];
  const hasSelectedChat = !!session && session.agent_id === selectedAgent;
  // Item 2ip (b): the page shows a live run's apparatus only while one is live.
  // Idle, it is one line -- what ended, when, and what it cost -- and the
  // apparatus is folded away rather than removed. The operator can open it, and
  // a run starting opens it for them.
  const live = isLiveRun(session);
  if (live) liveOpenedByOperator = false;
  const showApparatus = hasSelectedChat && (live || liveOpenedByOperator);
  liveContent.hidden = !showApparatus;
  liveIdle.hidden = !hasSelectedChat || showApparatus;
  if (!liveIdle.hidden) liveIdle.replaceChildren(...idleSummary(session));
  liveEmpty.hidden = hasSelectedChat || !store.loaded;
  renderStopState(panelStop, hasSelectedChat ? session : null, store.replay);
  panelLiveCompactions.textContent = hasSelectedChat ? compactionFigures(session) : "";
  panelLiveCompactions.hidden = !hasSelectedChat;
  document.getElementById("panel-live-state").textContent = !store.loaded ? "" : !hasSelectedChat ? "no open chat" : session.pending_approval ? "waiting for you" : liveActivityText(session) || session.run?.status || "idle";
  renderRunResult(hasSelectedChat ? session : null);
  if (hasSelectedChat && showApparatus) {
    renderRail(); renderFlow(); renderRack(); renderState(); renderTimeline(); placeDropLastMessage(); dropControl.render(); renderPendingApproval(session);
  }
  navigationSurfaceReady("panels", store);
}

// Item 2ip (b): "live" is a run that is doing something, not a chat that exists.
function isLiveRun(session) {
  return ["running", "queued", "stopping"].includes(session?.run?.status || "");
}

// Opened by hand, and only until the next run starts.
let liveOpenedByOperator = false;

// The one line: what ended, when, and what it cost. Every figure here is already
// on the page while a run is live; nothing new is computed for it.
function idleSummary(session) {
  const run = session?.run;
  const when = run?.ended_at ? new Date(run.ended_at).toLocaleTimeString() : "";
  const used = session?.context?.used_tokens;
  const window = session?.context?.window_tokens;
  const parts = ["idle"];
  if (when) parts.push(`last run ended ${when}`);
  if (Number.isFinite(used) && Number.isFinite(window) && window > 0) parts.push(`${used.toLocaleString()} / ${window.toLocaleString()}`);
  const text = node("span", "panel-live-idle-text");
  text.textContent = parts.join(" · ");
  const open = node("button", "panel-live-open");
  open.type = "button";
  open.textContent = "Show the run detail";
  open.addEventListener("click", () => { liveOpenedByOperator = true; scheduleRender(); });
  return [text, open];
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

// Item 2iq (a): one row per role the product has, each with the role's
// plain-language name and a model picker listing the connection profiles with
// state. Nothing else in the row.
//
// b is settable here and from the strip's switcher, through the one write route
// that exists. c and d are NOT settable: there is no route for them, and 2iq's
// @keep protects "the write route" and "the c/d semantics in the run loop" from
// this item. So their pickers show what is configured and say where it is set,
// rather than pretending to write. The finding is in the report.
const roleNames = { b: "the one you talk to", c: "the worker", d: "the planner" };

function renderAgentConnection(agent) {
  const connections = store.connections?.length ? store.connections : store.config.connections || [];
  const pending = store.agent_connection_changes?.[selectedAgent];
  const rows = [];
  for (const role of ["b", "c", "d"]) {
    const assigned = agent?.[role];
    // (a): d appears "when present" -- a product without a d role shows no d row.
    if (role === "d" && !assigned) continue;
    const settable = role === "b";
    const row = node("div", "panel-role-row");
    const name = node("span", "panel-role-name");
    name.textContent = role;
    const what = node("span", "panel-role-what");
    what.textContent = roleNames[role];
    const picker = document.createElement("select");
    picker.dataset.role = role;
    picker.setAttribute("aria-label", `${role} — ${roleNames[role]}`);
    picker.replaceChildren(...connections.map((connection) => option(connection.id, connection.label || connection.id, connection.id === assigned)));
    picker.disabled = !agent || store.replay || !settable;
    const state = node("span", "panel-role-state");
    if (settable) {
      state.textContent = !agent ? "" : pending ? `applied ${agent.b} · pending ${pending.to}` : "";
      state.className = `panel-role-state ${pending ? "pending" : ""}`;
    } else {
      state.textContent = "set in the configuration";
    }
    row.append(name, what, picker, state);
    if (settable && pending) {
      const cancel = node("button", "panel-role-cancel");
      cancel.type = "button";
      cancel.dataset.action = "cancel-pending";
      cancel.textContent = "Cancel pending";
      cancel.disabled = store.replay;
      row.append(cancel);
    }
    if (settable) {
      // The probe's vision verdict belongs to the connection this row names, so
      // it is built here rather than being a single page-level element moved
      // from row to row.
      const connection = connections.find((candidate) => candidate.id === assigned);
      if (connection?.capabilities?.probed_at) {
        const vision = connection.capabilities.vision || "not classified";
        const visionFinding = (connection.capabilities.findings || []).find((finding) => finding.startsWith("vision:"));
        const mark = node("span", `panel-agent-vision ${vision === "reads images" ? "reads" : "does-not-read"}`);
        mark.setAttribute("role", "img");
        mark.title = visionFinding || `vision: ${vision}`;
        mark.setAttribute("aria-label", mark.title);
        row.append(mark);
      }
    }
    rows.push(row);
  }
  roleTable.replaceChildren(...rows);
}

async function changeAgentConnection(connectionID) {
  if (!selectedAgent || store.replay) return;
  try {
    const result = await api(`/api/agents/${encodeURIComponent(selectedAgent)}/connection`, { action: "set", connection_id: connectionID });
    showFeedback(result.status === "pending" ? `Server change queued for ${selectedAgent}.` : `Server changed for ${selectedAgent}.`);
    await refreshState();
  } catch (error) { showError(error.message); scheduleRender(); }
}

async function cancelAgentConnectionChange() {
  if (!selectedAgent || store.replay) return;
  try {
    await api(`/api/agents/${encodeURIComponent(selectedAgent)}/connection`, { action: "cancel" });
    showFeedback(`Pending connection change cancelled for ${selectedAgent}.`);
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
	const configurable = (store.tools || []).filter((tool) => !["web_search", "delegate"].includes(tool.name));
	const configurableEnabled = configurable.filter((tool) => enabled.has(tool.name));
	document.getElementById("panel-tools-count").textContent = `${configurableEnabled.length} tools active`;
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
  const sections = [[selectedAgent, ledger.agent], ...Object.entries(ledger.connections || {})];
  root.replaceChildren(...sections.flatMap(([name, counters]) => [text(name, "panel-connection-head"), ...lifetimeRows(counters, percentile).map(([label, value]) => line(label, value))]));
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
