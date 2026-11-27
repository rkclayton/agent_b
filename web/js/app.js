import { api, reduce, setActive, store, subscribe } from "./bus.js";
import { renderRail } from "./rail.js";
import { renderFlow } from "./flow.js";
import { renderRack } from "./rack.js";
import { renderState } from "./state.js";
import { renderTimeline } from "./timeline.js";
import { createMessageDropController } from "./message-drop.js";
import { createApprovalCard } from "./approval.js";
import { loadReflection, loadReflectionNotes, wireReflectionNotes } from "./reflection.js";
import { activitySummary, agentKey, compactionFigures } from "./panel-lifetime.js";
import { renderStopState } from "./stop-state.js";
import { navigationSurfaceReady } from "./navigation-telemetry.js";
import { liveActivityText } from "./chat-activity.js";
import { requestConfirmation, requestText } from "./menu-behavior.js";

const requestedSession = new URLSearchParams(location.search).get("session");
let initialSession = requestedSession;
let selectedAgent = "";
let activity = null;
let ledgerRequest = 0;
let ledgerAgent = "";
let ledgerAsked = false;
let activityConfirmation = "";
let activityClearing = false;
let activityError = "";
const agentErrors = new Map();
let renderFrame = 0;
let mounted = false;
const liveContent = document.getElementById("panel-live-content");
const liveEmpty = document.getElementById("panel-live-empty");
const liveIdle = document.getElementById("panel-live-idle");
const dropLastMessage = document.getElementById("drop-last-message");
const agentSelect = document.getElementById("panel-agent");
const activityProfile = document.getElementById("activity-profile");
const activityFeedback = document.getElementById("activity-feedback");
const clearButton = document.getElementById("clear-stats");
const confirmClearButton = document.getElementById("activity-clear-confirm");
const cancelClearButton = document.getElementById("activity-clear-cancel");
const clearQuestion = document.getElementById("activity-clear-question");
const roleTable = document.getElementById("panel-roles");
const feedback = document.getElementById("panel-feedback");
const panelStop = document.getElementById("panel-stop");
const panelLiveCompactions = document.getElementById("panel-live-compactions");
const panelRunResult = document.getElementById("panel-run-result");
const panelRunStop = document.getElementById("panel-run-stop");
const panelRunLabel = document.getElementById("panel-run-label");

const dropControl = createMessageDropController(dropLastMessage, {
  session: () => store.sessions[store.active], interactive: () => !store.replay,
  confirmDrop: (message) => requestConfirmation(message, { confirmLabel: "Drop", danger: true }),
  drop: (id) => api(`/api/sessions/${encodeURIComponent(id)}/messages/drop-last`, {}), reportError: showError,
});

agentSelect.addEventListener("change", () => {
  selectedAgent = agentSelect.value;
  agentErrors.clear();
  const current = store.sessions[store.active];
  if (current?.agent_id !== selectedAgent) {
    const next = Object.values(store.sessions).filter((session) => !session.closed && session.agent_id === selectedAgent)
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0))[0];
    if (next) setActive(next.id);
  }
  void refreshLedger();
});
activityProfile.addEventListener("change", () => {
  selectedAgent = activityProfile.value;
  activityConfirmation = "";
  agentErrors.clear();
  void refreshLedger();
});
// b's row and the strip's switcher write the same setting, so
// the table delegates rather than binding a listener per render.
roleTable.addEventListener("change", (event) => {
  if (event.target instanceof HTMLSelectElement && event.target.dataset.role) void changeAgentConnection(event.target.value, event.target.dataset.role);
  else if (event.target instanceof HTMLSelectElement && event.target.dataset.field === "powershell") void editAgent("powershell");
  else if (event.target instanceof HTMLInputElement && event.target.dataset.field === "private") void editAgent("private");
});
roleTable.addEventListener("focusout", (event) => {
  const field = event.target instanceof HTMLElement ? event.target.dataset.field : "";
  if (field === "name" || field === "prompt") void editAgent(field);
});
roleTable.addEventListener("click", (event) => {
  const action = event.target instanceof HTMLElement ? event.target.dataset.action : "";
  if (action) void editAgent(action, event.target.dataset.value || "");
});
clearButton.addEventListener("click", () => { activityConfirmation = selectedAgent; renderLifetime(); confirmClearButton.focus(); });
cancelClearButton.addEventListener("click", () => { activityConfirmation = ""; renderLifetime(); clearButton.focus(); });
confirmClearButton.addEventListener("click", () => void clearStats());
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
    // on a direct load this is the first snapshot; the ledger is asked
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
  // the reflection section is read-only text; it is fetched on the
  // first snapshot and again after a run ends, when a new summary may exist.
  if (mounted && (event.type === "snapshot" || (event.type === "projection.patch" && patchEndedRun(event.data)))) {
    void loadReflection();
    // the rows refresh on the same events the overview does, and the
    // delegate is wired once.
    wireReflectionNotes();
    void loadReflectionNotes();
  }
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
  const sequence = ++ledgerRequest;
  const askedFor = selectedAgent;
  if (ledgerAgent !== askedFor || store.replay) {
    ledgerAgent = askedFor;
    activity = null;
    activityError = "";
    ledgerAsked = false;
  }
  if (!askedFor || store.replay) { ledgerAsked = store.loaded; if (render) scheduleRender(); return; }
  if (render) scheduleRender();
  try {
    const ledger = await api(`/api/stats/${encodeURIComponent(askedFor)}`, undefined, "GET");
    if (sequence !== ledgerRequest || askedFor !== selectedAgent) return;
    activity = activitySummary(ledger.agent);
    activityError = "";
    ledgerAsked = true;
  } catch (error) {
    if (sequence !== ledgerRequest || askedFor !== selectedAgent) return;
    activityError = error.message;
  }
  scheduleRender();
}

function renderPanels() {
  renderFrame = 0;
  if (!mounted) return;
  const agents = store.config.agents || [];
  if (!agents.some((agent) => agentKey(agent) === selectedAgent)) selectedAgent = agentKey(agents[0]);
  agentSelect.replaceChildren(...agents.map((agent) => option(agentKey(agent), agent.name, agentKey(agent) === selectedAgent)));
  activityProfile.replaceChildren(...agents.map((agent) => option(agentKey(agent), agent.name, agentKey(agent) === selectedAgent)));
  const agent = agents.find((candidate) => agentKey(candidate) === selectedAgent);
  renderAgentConnection(agent);
  document.getElementById("panel-agent-binding").textContent = agent ? "" : store.loaded ? "No configured agents" : "";
  renderTools(agent);
  renderLifetime();
  const session = store.sessions[store.active];
  const hasSelectedChat = !!session && session.agent_id === selectedAgent;
  // the page shows a live run's apparatus only while one is live.
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

// "live" is a run that is doing something, not a chat that exists.
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
  panelRunStop.textContent = `Ended: ${run.last_stop_reason}${run.last_stop_detail ? ` — ${run.last_stop_detail}` : ""}`;
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

// one row per role the product has, each with the role's
// plain-language name and a model picker listing the connection profiles with
// state. Nothing else in the row.
//
// b is settable here and from the strip's switcher, through the one write route
// that exists. c and d are NOT settable: there is no route for them, and 2iq's
// @keep protects "the write route" and "the c/d semantics in the run loop" from
// this item. So their pickers show what is configured and say where it is set,
// rather than pretending to write. The finding is in the report.
const roleNames = { b: "the one you talk to", c: "the worker", d: "the planner" };

// what a role's change affects, in the same words the run loop
// obeys. measured it rather than assuming:
//
//   - a run resolves its connection from the SESSION, so a session already bound
//     is never moved by a role change;
//   - but r.cfg is a live accessor, and the aux and compaction paths read
//     agent.C at the point of use, so a compaction taken after the change uses
//     the new one;
//   - b alone is deferred until the agent is idle.
//
// So b says "pending" when it is, and c and d say what is true of them: the next
// run that reads them.
const roleEffect = {
  b: "changes when this agent is idle",
  c: "takes effect on the next run that reads it",
  d: "takes effect on the next run that reads it",
};

function renderAgentConnection(agent) {
  // The live projection can briefly lag config after a connection is saved. The
  // picker must still offer every configured connection so the operator can
  // move a role away from one that is about to be removed.
  const connections = store.config.connections?.length ? store.config.connections : store.connections || [];
  if (!agent) { roleTable.replaceChildren(); return; }
  const builtIn = selectedAgent === "agent_b";
  const field = (label, control, errorKey = "") => {
    const row = node("label", "panel-role-row"); row.append(text(label, "panel-role-name"), control);
    const message = agentErrors.get(errorKey);
    if (message) { const error = text(message, "field-error panel-role-error"); error.dataset.agentError = errorKey; row.append(error); }
    return row;
  };
  const name = document.createElement("input"); name.value = agent.name || ""; name.dataset.field = "name"; name.disabled = builtIn;
  const prompt = document.createElement("textarea"); prompt.value = builtIn ? "Shipped opening prompt" : (agent.prompt || ""); prompt.dataset.field = "prompt"; prompt.disabled = builtIn;
  const helper = (role) => {
    const select = document.createElement("select"); select.dataset.role = role;
    select.append(option("", role === "c" ? "chat connection" : "not assigned", !agent[role]));
    select.append(...connections.map((value) => option(value.id, value.label || value.id, value.id === agent[role])));
    return field(roleNames[role], select);
  };
  const thinking = button("", "thinking"); thinking.type = "button"; thinking.dataset.action = "thinking"; thinking.setAttribute("role", "switch"); thinking.setAttribute("aria-checked", String(!!agent.thinking)); thinking.className = `switch ${agent.thinking ? "on" : ""}`;
  const effortOrder = ["low", "medium", "high"];
  const offeredEfforts = new Set((store.config.connections || []).flatMap((connection) => connection.reasoning?.valid_efforts || connection.capabilities?.valid_efforts || []));
  const effort = node("span", "choice-row profile-effort");
  for (const value of ["", ...effortOrder.filter((value) => offeredEfforts.has(value))]) {
    const choice = button(value || "model default", value || "model default"); choice.dataset.action = "effort"; choice.dataset.value = value;
    if ((agent.effort || "") === value) choice.classList.add("selected");
    effort.append(choice);
  }
  const thinkingControl = node("span", "profile-thinking-control"); thinkingControl.append(thinking); if (agent.thinking) thinkingControl.append(effort);
  const powershell = document.createElement("select"); powershell.dataset.field = "powershell";
  powershell.append(option("", "Off", !agent.powershell), option("read", "Read only", agent.powershell === "read"), option("full", "Full", agent.powershell === "full"));
  const rows = [field("name", name, "name"), field("thinking", thinkingControl, "thinking"), field("persistent PowerShell", powershell, "powershell"), field("opening prompt", prompt, "prompt"), helper("c"), helper("d")];
  if (!builtIn) {
    const privateToggle = document.createElement("input"); privateToggle.type = "checkbox"; privateToggle.checked = !!agent.private; privateToggle.dataset.field = "private";
    privateToggle.title = "this agent only writes to its own memory but can still read shared";
    rows.push(field("private", privateToggle, "private"));
  }
  const actions = node("div", "panel-role-row settings-actions");
  for (const [action, label] of [["add", "Add"], ["duplicate", "Duplicate"], ["delete", "Delete"]]) {
    const control = button(label, label); control.dataset.action = action; control.disabled = store.replay || (builtIn && action === "delete"); actions.append(control);
  }
  rows.push(actions);
  roleTable.replaceChildren(...rows);
}

async function editAgent(action, value = "") {
  const agent = (store.config.agents || []).find((value) => agentKey(value) === selectedAgent);
  if (!agent) return;
  const read = (field) => roleTable.querySelector(`[data-field="${field}"]`);
  let body;
  const errorKey = action === "effort" ? "thinking" : action;
  agentErrors.delete(errorKey);
  if (action === "thinking") {
    body = { action: "update", id: selectedAgent, thinking: !agent.thinking };
  } else if (action === "effort") {
    body = { action: "update", id: selectedAgent, effort: value };
  } else if (action === "name") {
    const name = read("name")?.value;
    if (name === undefined || name === agent.name) return;
    body = { action: "update", id: selectedAgent, name };
  } else if (action === "prompt") {
    const prompt = read("prompt")?.value;
    if (prompt === undefined || prompt === (agent.prompt || "")) return;
    body = { action: "update", id: selectedAgent, prompt };
  } else if (action === "private") {
    body = { action: "update", id: selectedAgent, private: !!read("private")?.checked };
  } else if (action === "powershell") {
    body = { action: "update", id: selectedAgent, powershell: read("powershell")?.value || "" };
  } else if (action === "add") {
    const name = await requestText("Agent name"); if (!name) return;
    body = { action: "add", name };
  } else if (action === "duplicate") {
    const name = await requestText("Duplicate name"); if (!name) return;
    body = { action: "duplicate", source_id: selectedAgent, name };
  } else if (action === "delete") {
    if (!await requestConfirmation(`Delete ${agent.name}?`, { confirmLabel: "Delete", danger: true })) return;
    body = { action: "delete", id: selectedAgent };
  } else return;
  try {
    const result = await api("/api/agents", body);
    if (result.agent_id) selectedAgent = result.agent_id;
    reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
  } catch (error) { agentErrors.set(errorKey, error.message); scheduleRender(); }
}

async function changeAgentConnection(connectionID, role = "b") {
  if (!selectedAgent || store.replay) return;
  try {
    const result = await api(`/api/agents/${encodeURIComponent(selectedAgent)}/connection`, { action: "set", role, connection_id: connectionID });
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

function renderTools(agent) {
  const root = document.getElementById("panel-tools");
  if (!agent) {
    root.innerHTML = store.loaded ? '<p class="panel-empty">No agent selected.</p>' : "";
    return;
  }
	const enabled = new Set(agent.toolset || []);
	// web_search is configured only in harness.json for this release.
	const configurable = (store.tools || []).filter((tool) => !["web_search", "delegate", "powershell_session"].includes(tool.name));
	const configurableEnabled = configurable.filter((tool) => enabled.has(tool.name));
	document.getElementById("panel-tools-count").textContent = `${configurableEnabled.length} tools active`;
	root.replaceChildren(...configurable.map((tool) => {
		const row = node("details", "panel-tool-line");
		row.title = tool.description || tool.name;
		const summary = document.createElement("summary");
    const toggle = document.createElement("input");
		toggle.type = "checkbox"; toggle.checked = enabled.has(tool.name); toggle.dataset.tool = tool.name; toggle.disabled = store.replay || selectedAgent === "agent_b";
		summary.append(toggle, text(tool.name));
		const description = node("div", "panel-tool-description");
		description.textContent = tool.description || "";
		row.append(summary, description);
    return row;
  }));
}

let drawnActivity;
function renderLifetime() {
  const root = document.getElementById("panel-stats");
  const current = ledgerAgent === selectedAgent ? activity : null;
  root.hidden = !current;
  document.getElementById("activity-empty").hidden = !store.loaded || !!current || !ledgerAsked || !!activityError;
  activityFeedback.textContent = activityError;
  if (drawnActivity !== current) {
    for (const kind of ["average", "overall"]) {
      document.getElementById(`activity-${kind}`).replaceChildren(...(current?.[kind] || []).map(([label, value]) => {
        const row = node("tr"), name = node("th"), result = node("td");
        name.scope = "row";
        name.textContent = label;
        result.textContent = value;
        row.append(name, result);
        return row;
      }));
    }
    document.getElementById("activity-since").textContent = current?.since || "";
    drawnActivity = current;
  }
  const confirming = activityConfirmation === selectedAgent && !!selectedAgent;
  clearButton.hidden = confirming || store.replay;
  clearQuestion.hidden = confirmClearButton.hidden = cancelClearButton.hidden = !confirming;
  clearQuestion.textContent = confirming ? `Clear all stats for ${activityProfile.selectedOptions[0]?.textContent || selectedAgent}?` : "";
  confirmClearButton.disabled = cancelClearButton.disabled = activityClearing;
}

async function toggleTool(event) {
  const name = event.target?.dataset?.tool;
  if (!name || !selectedAgent) return;
  try { await api(`/api/tools/${encodeURIComponent(name)}`, { agent_id: selectedAgent, enabled: event.target.checked }); await refreshState(); }
  catch (error) { event.target.checked = !event.target.checked; showError(error.message); }
}

async function clearStats() {
  const agent = selectedAgent;
  if (!agent || store.replay || activityClearing || activityConfirmation !== agent) return;
  activityClearing = true;
  renderLifetime();
  try {
    const ledger = await api(`/api/stats/${encodeURIComponent(agent)}/clear`, { confirm: true });
    if (agent === selectedAgent) {
      ledgerRequest++;
      activity = activitySummary(ledger.agent);
      ledgerAsked = true;
      activityError = "";
      activityConfirmation = "";
    }
  } catch (error) {
    if (agent === selectedAgent) activityError = error.message;
  } finally {
    activityClearing = false;
    renderLifetime();
  }
}

async function flushMemory() {
  const session = Object.values(store.sessions).find((item) => item.agent_id === selectedAgent && !item.closed) || store.sessions[store.active];
  if (!session || store.replay) return showError("An open chat is required to identify the folder.");
  try {
    const preview = await api(`/api/agents/${encodeURIComponent(selectedAgent)}/memory/flush`, { workspace: session.workspace, confirm: false });
    if (!await requestConfirmation(`Flush memory for ${selectedAgent} and ${preview.workspace}?\n\n${preview.agent_entries} agent entries and ${preview.workspace_entries} folder entries will be removed.`, { confirmLabel: "Flush", danger: true })) return;
    await api(`/api/agents/${encodeURIComponent(selectedAgent)}/memory/flush`, { workspace: session.workspace, confirm: true });
    showFeedback(`Memory flushed for ${selectedAgent} and ${preview.workspace}.`); await refreshState();
  } catch (error) { showError(error.message); }
}

async function refreshState() { reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); await refreshLedger(); }
function placeDropLastMessage() { const heads = document.querySelectorAll(".timeline-model > .timeline-head"); const target = heads[heads.length - 1]; dropLastMessage.hidden = !target; if (target) target.append(dropLastMessage); }
function renderPendingApproval(session) { const root = document.getElementById("panel-pending-approval"); root.hidden = !session?.pending_approval; root.replaceChildren(...(session?.pending_approval ? [createApprovalCard(document, session.pending_approval, { replay: store.replay, decide: (callID, decision, answer) => api("/api/approve", { session_id: session.id, call_id: callID, decision, answer }) })] : [])); }
function patchEndedRun(patch = {}) { return (patch.operations || []).some((operation) => operation.path === "/run" && operation.value?.status === "idle"); }
// lifetime and the last twenty, side by side in ONE value cell.
//
// My first version gave the row a third grid column. Activity flows its lines
// into narrow columns, and three cells did not fit: the screenshot showed
// "lifetime" and "last 20" printed on top of "runs measured". The panel has two
// columns everywhere else for a reason, so this keeps two and separates the
// figures the way the rest of the panel already separates a pair.
function runProfileSection(rows) {
  if (!rows.length) return [];
  // each figure sits in a seat the width of its widest form, so a
  // value that gains a digit changes nothing about where the other column, or
  // the column beside this panel, ends up.
  return [line("per run", "lifetime · last 20"), ...rows.map(([label, life, recent, seat]) => {
    const row = node("div", "panel-line");
    row.append(text(label));
    const value = node("span", "panel-figures");
    value.append(seated(life, seat), text(" · "), seated(recent, seat));
    row.append(value);
    return row;
  })];
}

// seated wraps one measured value in a fixed-width, right-aligned, tabular-figure
// box. Tabular figures alone are not enough: they make every DIGIT the same
// width, and the defect is a changing NUMBER of digits.
function seated(value, characters) {
  const span = node("span", "seat");
  span.style.setProperty("--seat", `${characters}ch`);
  span.append(text(String(value)));
  return span;
}

function line(label, value) { const row = node("div", "panel-line"); row.append(text(label), text(String(value))); return row; }
function node(tag, className = "") { const value = document.createElement(tag); value.className = className; return value; }
function text(value, className = "") { const result = node("span", className); result.textContent = String(value); return result; }
function button(value, title) { const result = node("button"); result.type = "button"; result.textContent = value; result.title = title; return result; }
function option(value, label, selected) { const result = document.createElement("option"); result.value = value; result.textContent = label; result.selected = selected; return result; }
function showFeedback(message) { feedback.textContent = message; feedback.className = ""; }
function showError(message) { feedback.textContent = message; feedback.className = "alarm"; const connection = document.getElementById("connection"); if (connection) { connection.textContent = message; connection.className = "alarm"; } }
