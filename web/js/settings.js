import { api, reduce, setActive, store, subscribe } from "./bus.js";
import { operatorStatusView } from "./operator-status.js";
import { navigationSurfaceReady, recordViewMount } from "./navigation-telemetry.js";
import { renderConnectionsPage } from "./settings-connections.js";
import { renderAboutPage } from "./settings-about.js";
import { renderChatsPage } from "./settings-chats.js";
import { renderGeneralPage } from "./settings-general.js";
import { renderNotificationsPage } from "./settings-notifications.js";
import { renderProfilesPage } from "./settings-profiles.js";
import { renderSecurityPage } from "./settings-security.js";
import { renderWorkspacePage } from "./settings-workspace.js";
import { mountPanels, unmountPanels } from "./app.js";

const sheet = document.getElementById("settings-page");
let gear;
const expanded = new Set();
const advancedConnections = new Set();
const armed = new Set();
const drafts = new Map();
const draftKinds = new Map();
const errors = new Map();
const probeMessages = new Map();
const shownKeys = new Set();
let open = false;
let lastFocus = null;
let shellCredentialMessage = "";
let shellCredentialAlarm = false;
let serviceAccountStatus = { loaded: false, supported: true, exists: false, administrator: false };
let serviceAccountBusy = false;
let serviceAccountMessage = "";
let serviceAccountAlarm = false;
let hardeningStatus = { loaded: false, supported: true, applied: false };
let hardeningBusy = false;
let hardeningMessage = "";
let hardeningAlarm = false;
let notificationStatus = { configured: false, host: "" };
let notificationBusy = false;
let notificationMessage = "";
let notificationAlarm = false;
let settingsSaving = false;
let settingsSaveMessage = "All changes saved";
let settingsSaveAlarm = false;
let activeSection = "connections";
// The view another document was on when it sent us here; "" when the sheet was
// opened from inside this one, where it closes onto the chat beneath it.
let openedFrom = "";
let hardeningConnectionID = "";
let workspaceState = [];
let operatorFileState = { attachment_files: 0, attachment_bytes: 0, instruction_found: [] };
let phoneAccess = { devices: [], code: "", expires_at: "", push_enabled: false };
const connectionList = () => Array.isArray(store.connections) ? store.connections : [];

// Item 2gk: Agents and Activity are where the page that used to stand on its
// own now lives. They come first because they are what the operator opened
// that page to read.
const sectionLabels = [
  ["agents", "Agents"],
  ["activity", "Activity"],
  ["connections", "Connections"],
  ["profiles", "Profiles"],
  ["chats", "Chats"],
  ["notifications", "Notifications"],
  ["shell", "Security"],
  ["about", "About"],
];

// Item 2hb (v1.2.4): `entry` is what the address asked for, read once by
// workspace.js before the shell rewrote it. `entry.from` is the view another
// document was on when its gear was clicked, and closing returns there.
export function initSettings(entry = {}) {
  openedFrom = entry.from || "";
  gear = document.querySelector(".shell-settings");
  gear.addEventListener("click", (event) => {
    event.preventDefault();
		open ? void leaveSettingsForChat() : openSettings();
  });
  document.addEventListener("settings.open", (event) => openSettings(event.detail?.section));
  // Choosing a chat from the tab strip while Settings is open shows that chat.
  document.addEventListener("settings.close", (event) => {
		if (open) void leaveSettingsForChat().finally(() => event.detail?.after?.());
	});
  document.addEventListener("keydown", (event) => {
		// Item 2l4 (c): Escape answers the popover first, and cancels it.
		if (event.key === "Escape" && open && confirmPending) { cancelConfirmation(); return; }
		if (event.key === "Escape" && open) void leaveSettingsForChat();
    if (open && (event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
      event.preventDefault();
      saveSettings();
    }
  });
  sheet.addEventListener("click", click);
  // Item 2l4 (c): a click outside cancels. The popover and the control that raised
  // it are the only places a click means something else.
  sheet.addEventListener("pointerdown", (event) => {
    if (!confirmPending) return;
    if (event.target.closest(".confirm-popover") || event.target.closest("[data-action]")) return;
    cancelConfirmation();
  }, true);
  sheet.addEventListener("focusout", blur);
  sheet.addEventListener("change", change);
  sheet.addEventListener("toggle", (event) => {
    if (!event.target.matches("details[data-connection-advanced]")) return;
    event.target.open ? advancedConnections.add(event.target.dataset.connectionAdvanced) : advancedConnections.delete(event.target.dataset.connectionAdvanced);
  }, true);
  sheet.addEventListener("input", (event) => {
    if (event.target.matches(".setting-input[data-path]")) {
      const path = event.target.dataset.path;
      drafts.set(path, event.target.value);
      draftKinds.set(path, event.target.dataset.kind || "text");
      // Item 2l6 (b): typing is not committing. The draft is held so the field
      // keeps what is typed, and blur applies it; a setting that waits for an
      // explicit save says so now, and the rest say nothing until they apply.
      appliedSettings.delete(path);
      settingsSaveMessage = needsExplicitSave(path) ? "Unsaved — use the save beside the setting" : "";
      settingsSaveAlarm = false;
      refreshSaveControls();
    }
  });
  subscribe((_state, event) => {
	if (event.type === "notification.changed") notificationStatus = event.data || notificationStatus;
    if (event.type === "connection.probed") {
      const connectionID = event.data?.connection_id || "";
      const findings = event.data?.capabilities?.findings || event.data?.findings || [];
      const failed = findings.find((value) => String(value).startsWith("probe failed:"));
      probeMessages.set(connectionID, { ...(probeMessages.get(connectionID) || {}), message: failed ? `Test failed — ${String(failed).slice(13).trim()}` : "Test passed", alarm: !!failed });
    }
    if (
      open && (
      [
        "init",
        "snapshot",
        "active.changed",
        "config.changed",
		"notification.changed",
		"update.changed",
		"shell.identity",
		"shell.credential",
        "connection.probed",
      ].includes(event.type) || (event.type === "projection.patch" && (event.data?.operations || []).some((operation) =>
        ["/label", "/agent_id", "/connection_id", "/agent_name", "/b_connection", "/role", "/plan_id", "/plan_name", "/runnable", "/not_runnable_reason", "/tools", "/memory_path", "/memory_content", "/agent_memory_path", "/agent_memory_content", "/budget", "/closed"].includes(operation.path))))
    )
      render();
    if (open && event.type === "snapshot") {
      refreshServiceAccountStatus();
      refreshHardeningStatus();
	  refreshNotificationStatus();
	  refreshOperatorFileState();
    }
  });
}

export function openSettings(section = "") {
  const started = performance.now();
  if (sectionLabels.some(([id]) => id === section)) activeSection = section;
  open = true;
  lastFocus = document.activeElement;
  // Item 2gk: app.css dresses this sheet and the two panels it adopts, and
  // nothing else on screen, so it is switched on with the sheet.
  setSheetStyles(true);
  sheet.hidden = false;
  sheet.setAttribute("aria-hidden", "false");
  gear.setAttribute("aria-expanded", "true");
  gear.setAttribute("aria-label", "Close settings");
  gear.setAttribute("aria-pressed", "true");
  gear.classList.add("selected");
  history.replaceState(null, "", `#settings/${activeSection}`);
  render();
  refreshServiceAccountStatus();
	refreshHardeningStatus();
	refreshNotificationStatus();
	refreshPhoneAccess();
  refreshWorkspaceState();
  refreshOperatorFileState();
  requestAnimationFrame(() => sheet.querySelector(".settings-nav button.selected")?.focus());
  recordViewMount("settings", performance.now() - started);
}

export function closeSettings(surface = "chat") {
  if (!open) return;
  open = false;
  // The panels go home before the sheet is hidden, so they are never left
  // inside a hidden surface where the next render would wipe them.
  returnAdoptedPanels();
  unmountPanels();
  setSheetStyles(false);
  sheet.hidden = true;
  sheet.setAttribute("aria-hidden", "true");
  gear.setAttribute("aria-expanded", "false");
  gear.setAttribute("aria-label", "Settings");
  gear.setAttribute("aria-pressed", "false");
  gear.classList.remove("selected");
  history.replaceState(null, "", `${location.pathname}${location.search}`);
  (lastFocus || gear).focus();
  navigationSurfaceReady(surface, store);
  // Item 2hb: Settings closes to the view it opened from. From inside this
  // document that is the chat, which is already beneath it; from Plan it is
  // another document, so closing goes back rather than leaving the operator
  // somewhere he never asked for.
  if (openedFrom === "plan") {
    const session = new URLSearchParams(location.search).get("session");
    openedFrom = "";
    location.assign(`/plan${session ? `?session=${encodeURIComponent(session)}` : ""}`);
  }
}

async function leaveSettingsForChat() {
	// The question is save-or-discard, not stay-or-leave: either answer honors
	// the requested Chat navigation. A failed save is already rendered by the
	// settings save path and does not trap the operator on this surface.
	// (e): nothing else can be lost by navigating away, because nothing else is
	// pending — every other setting applied when it was set.
	if (pendingExplicitSaves().length && (globalThis.confirm?.("Save unsaved settings before returning to Chat?") ?? false)) {
		for (const path of pendingExplicitSaves()) await saveSettings(path);
	}
	openedFrom = "";
	closeSettings("chat");
}

function render() {
  const active = store.sessions[store.active];
  const scrollTop = sheet.querySelector(".settings-content")?.scrollTop || 0;
  const focusKey = controlKey(document.activeElement);
  const secretValues = new Map(
    [...sheet.querySelectorAll('input[type="password"]')]
      .map((input) => [controlKey(input), input.value])
      .filter(([key, value]) => key && value),
  );
  const content = {
    // The two adopted panels are drawn by app.js, not here: this leaves the
    // seat and the nodes are moved into it below.
    agents: () => '<div data-adopt="agents-panel"></div>',
    activity: () => '<div data-adopt="activity-panel"></div>',
    connections: () => renderConnectionsPage(settingsPageContext(active)),
    profiles: () => renderProfilesPage(settingsPageContext(active)),
    sessions: () => renderGeneralPage("sessions", active, settingsPageContext(active)),
    tools: () => renderGeneralPage("tools", active, settingsPageContext(active)),
    memory: () => renderGeneralPage("memory", active, settingsPageContext(active)),
    chats: () => renderChatsPage(active, settingsPageContext(active)),
    notifications: () => renderNotificationsPage(settingsPageContext(active)),
    shell: () => renderSecurityPage("shell", active, settingsPageContext(active)) + renderWorkspacePage(settingsPageContext(active)),
    about: () => renderAboutPage(settingsPageContext(active)),
    session: () => renderSecurityPage("session", active, settingsPageContext(active)),
  };
  const label = sectionLabels.find(([id]) => id === activeSection)?.[1] || "Settings";
  // Item 2gk: an adopted panel is put back in its source holder before the
  // sheet is rewritten. Assigning innerHTML destroys whatever is inside, and
  // the panels are the only nodes here that cannot be rebuilt from a string.
  returnAdoptedPanels();
  sheet.innerHTML = `
    <header class="settings-head">
      <div><strong>Settings</strong><span data-save-status class="${settingsSaveAlarm ? "alarm" : ""}">${html(settingsSaveMessage)}</span></div>
      <div class="settings-head-actions"></div>
    </header>
    <div class="settings-layout">
      <nav class="settings-nav" aria-label="Settings sections">
        ${sectionLabels.map(([id, name]) => `<button type="button" class="${id === activeSection ? "selected" : ""}" data-action="settings-section" data-id="${id}" aria-current="${id === activeSection ? "page" : "false"}">${name}</button>`).join("")}
      </nav>
      <div class="settings-content" tabindex="-1">${group(label, content[activeSection]())}</div>
    </div>${confirmPopover()}`;
  adoptPanels();
  const contentNode = sheet.querySelector(".settings-content");
  contentNode.scrollTop = scrollTop;
  for (const input of sheet.querySelectorAll('input[type="password"]')) {
    const value = secretValues.get(controlKey(input));
    if (value) input.value = value;
  }
  const focusNode = [...sheet.querySelectorAll("button, input, textarea, select, summary")]
    .find((node) => controlKey(node) === focusKey);
  focusNode?.focus({ preventScroll: true });
  navigationSurfaceReady("settings", store);
}

// Item 2gk: the two panels are MOVED between their source holder and the open
// sheet. Moving rather than copying is the whole point — the nodes carry
// the listeners app.js set, and the renderers write into them by id, so a rebuilt
// copy would be a second, dead set of controls.
function setSheetStyles(on) {
  const styles = document.getElementById("panel-styles");
  if (styles) styles.disabled = !on;
}

function returnAdoptedPanels() {
  const sources = document.getElementById("panel-sources");
  if (!sources) return;
  for (const panel of sheet.querySelectorAll("#agents-panel, #activity-panel")) sources.append(panel);
}

function adoptPanels() {
  let adopted = false;
  for (const slot of sheet.querySelectorAll("[data-adopt]")) {
    const panel = document.getElementById(slot.dataset.adopt);
    if (!panel) continue;
    slot.append(panel);
    adopted = true;
  }
  // The renderers run only while a panel is on screen; off it they would draw
  // into nodes nobody can see, once per event.
  if (adopted) mountPanels();
  else unmountPanels();
}

function settingsPageContext(active) {
  return {
    active, store, expanded, advancedConnections, armed, drafts, errors, probeMessages, workspaceState, operatorFileState, phoneAccess, standingGrants: store.standing_grants || [],
    shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy,
    serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage,
    hardeningAlarm, connectionList,
    notificationStatus, notificationBusy, notificationMessage, notificationAlarm,
    row, subhead, field, text, number, numberControl, textarea, secret, toggle, choices, approvalChoices,
    copyRow, currentValue, issue, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView,
  };
}

// Item 2l6 (d): there is no sheet-wide Save to refresh any more. The status line
// is what remains, and it says what the last commit did.
function refreshSaveControls() {
  const status = sheet.querySelector("[data-save-status]");
  if (!status) return;
  status.textContent = settingsSaveMessage;
  status.classList.toggle("alarm", settingsSaveAlarm);
}
function controlKey(node) {
  if (!node || !sheet.contains(node)) return "";
  return node.id || node.dataset?.path || [node.dataset?.action, node.dataset?.id || node.dataset?.setupAction].filter(Boolean).join(":") || node.getAttribute?.("aria-label") || "";
}

function group(name, content) {
  return `<section class="settings-group"><h2>${name}</h2>${content}</section>`;
}

async function refreshWorkspaceState() {
	try { workspaceState=await api("/api/workspaces",undefined,"GET") } catch { workspaceState=[] }
	if(open&&activeSection==="shell")render();
}

async function refreshOperatorFileState() {
	const dir=store.sessions[store.active]?.workspace||store.config.workspace||"";
	try { operatorFileState=await api(`/api/operator-files?dir=${encodeURIComponent(dir)}`,undefined,"GET") }
	catch { operatorFileState={attachment_files:0,attachment_bytes:0,instruction_found:[]} }
	if(open&&activeSection==="shell")render();
}

function row(label, control, extra = "", hint = "") {
  const hover = hint || `${label} setting.`;
  return `<div class="setting-row ${extra}" title="${attr(hover)}"><label>${html(label)}</label><div>${control}</div></div>`;
}

// A subsection heading carries the paragraph that used to sit under it.
function subhead(label, hint = "") {
  return `<div class="settings-subhead">${html(label)}</div>${hint ? `<p class="settings-subhead-note">${html(hint)}</p>` : ""}`;
}

// Item 2l1 (a5) and (a6): the one action proposes what it learned as SOFT values
// in the fields — context size, output reserve, reasoning cap, effort and whether
// thinking is supported — and proposes into a field the operator has NOT changed,
// so pressing it again is safe and nothing typed is overwritten. (a3): a server
// offering exactly one model has it selected. Nothing here saves.
function applyProposedValues(id, discovered) {
  const proposed = discovered?.proposed;
  if (!proposed) return;
  const prefix = `connections.${id}.`;
  const connection = connectionList().find((item) => item.id === id) || {};
  const propose = (path, value, kind, saved) => {
    if (value === undefined || value === null || value === "") return;
    if (drafts.has(prefix + path)) return;                       // the operator typed it
    if (saved !== undefined && saved !== null && saved !== "" && saved !== 0) return; // he saved it
    drafts.set(prefix + path, String(value));
    draftKinds.set(prefix + path, kind);
    proposedFields.add(prefix + path);
  };
  // (a3): the previously selected model STAYS selected when it is still offered —
  // a saved, working model is never replaced by the server listing, which would
  // silently rewrite a display name to a path and undo a tested connection. Only
  // an empty field is filled, and only when the server offers exactly one model.
  const models = Array.isArray(discovered.models) ? discovered.models : [];
  const saved = (connection.model || "").trim();
  const keeps = saved !== "" && (models.length === 0 || models.includes(saved));
  if (!keeps && !drafts.has(prefix + "model") && proposed.model_selected) {
    drafts.set(prefix + "model", proposed.model_selected);
    draftKinds.set(prefix + "model", "text");
  }
  if (proposed.context_source === "published" || proposed.context_source === "probed") {
    propose("context.n_ctx", proposed.n_ctx, "number", connection.context?.n_ctx);
    propose("context.reserve_output", proposed.reserve_output, "number", connection.context?.reserve_output === 10240 ? 0 : connection.context?.reserve_output);
    propose("reasoning.max_tokens", proposed.reasoning_max_tokens, "number", connection.reasoning?.max_tokens);
  }
  if (Array.isArray(proposed.valid_efforts) && proposed.effort) propose("reasoning.effort", proposed.effort, "text", "");
  if (proposedFields.size) settingsSaveMessage = "Proposed values are unsaved — review and Save";
}

const proposedFields = new Set();
// Item 2l6 (b): which rows have just taken effect, so the row can say so.
const appliedSettings = new Map();
// Item 2l4: one anchored confirmation for every remove control. The operator:
// "the delete function the confirm is goofy dont change buttons like that in the
// same place, little pop up is fine very minimalistic." The two-click protocol the
// handlers already use is unchanged — the popover IS the second click — so every
// control that arms gets this behaviour without its handler being touched.
let confirmPending = null;
// Item 2l5 uses the same floppy for a connection's save; one glyph, one meaning.
const saveGlyph = "<svg viewBox=\"0 0 16 16\" width=\"13\" height=\"13\" aria-hidden=\"true\" focusable=\"false\"><path d=\"M2 2h9l3 3v9H2V2Zm2 1v4h6V3H4Zm1 7h6v3H5v-3Z\"/></svg>";

function current(path, fallback) {
  return drafts.has(path) ? drafts.get(path) : fallback ?? "";
}

function currentValue(path, fallback) {
  if (!drafts.has(path)) return fallback;
  return draftValue(drafts.get(path), draftKinds.get(path));
}

function issue(path) {
  for (const [field, message] of errors) {
    if (field === path || field.startsWith(`${path}.`) || path.startsWith(`${field}.`))
      return message;
  }
  return "";
}

// Item 2l6 (a) and (c): the settings that need a COMPLETE value before they mean
// anything keep an explicit save. A half-typed path is a different location, and a
// half-typed comma list silently narrows or widens a guard. Everything else in the
// inventory is a bounded number, a toggle or a choice from a fixed list, and applies
// the moment it is set. Connection values ride [[2l5]]'s per-connection save.
const explicitSavePaths = new Set(["memory.dir", "tools.list_dir.ignore", "tools.shell.operator_commands", "shell.deny"]);
function needsExplicitSave(path) {
  return explicitSavePaths.has(path) || path.startsWith("connections.");
}
function pendingExplicitSaves() {
  return [...drafts.keys()].filter(needsExplicitSave);
}

// applySetting writes one setting the moment it is committed — blur, toggle or
// selection, never per keystroke. An invalid value does not apply: the server says
// which field and why, that sits beside the field, and the stored value is left
// alone because nothing else was sent.
async function saveConnection(id) {
  const prefix = `connections.${id}.`;
  if (![...drafts.keys()].some((path) => path.startsWith(prefix))) return;
  if (await saveSettings(prefix)) settingsSaveMessage = "";
  if (open) render();
}

async function applySetting(path) {
  if (!drafts.has(path)) return;
  appliedSettings.delete(path);
  const ok = await saveSettings(path);
  if (ok) {
    appliedSettings.set(path, Date.now());
    settingsSaveMessage = "";
  }
  if (open) render();
}

function field(path, label, control, alarm = false, hint = "") {
  const problem = issue(path);
  // (b): the row shows it took effect. (c): a setting that waits for an explicit
  // save says so, with the save beside it rather than across the whole sheet.
  const state = problem ? "" : appliedSettings.has(path)
    ? '<span class="setting-applied" aria-live="polite">applied</span>'
    : needsExplicitSave(path) && drafts.has(path)
      ? `<button type="button" class="setting-save" data-action="save-setting" data-save-path="${attr(path)}" title="Save this setting" aria-label="Save this setting">${saveGlyph}</button>`
      : "";
  return `${row(label, control + state, alarm || problem ? "invalid" : "", hint)}${problem ? `<p class="field-error">${html(problem)}</p>` : ""}`;
}

function text(path, label, value, kind = "text", hint = "") {
  return field(path, label, `<input class="setting-input" data-path="${attr(path)}" data-kind="${kind}" value="${attr(current(path, value))}">`, false, hint);
}

function number(path, label, value, step = "1", disabled = false, note = "", alarm = false, kind = "number", hint = "") {
  const control = `${numberControl(path, value, step, disabled, kind)}${note ? `<span class="control-note">${html(note)}</span>` : ""}`;
  return field(path, label, control, alarm, hint);
}

function numberControl(path, value, step = "1", disabled = false, kind = "number") {
  return `<input class="setting-input number" type="number" step="${step}" data-path="${attr(path)}" data-kind="${kind}" value="${attr(current(path, value))}" ${disabled ? "disabled" : ""}>`;
}

function textarea(path, label, value, hint = "") {
  return field(path, label, `<textarea class="setting-input" rows="8" data-path="${attr(path)}" data-kind="text">${html(current(path, value))}</textarea>`, false, hint);
}

function secret(path, label, value, id, hint = "") {
  const shown = shownKeys.has(id);
  const type = shown ? "text" : "password";
  return field(path, label, `<span class="secret-control"><input class="setting-input" type="${type}" data-path="${attr(path)}" data-kind="secret" value="${attr(current(path, value))}"><button type="button" data-action="show-key" data-id="${attr(id)}">${shown ? "hide" : "show"}</button></span>`, false, hint);
}

function toggle(path, label, value, hint = "") {
  const selected = !!currentValue(path, value);
  return field(path, label, `<button type="button" role="switch" aria-checked="${selected}" aria-label="${attr(label)}" class="switch ${selected ? "on" : ""}" data-action="config-toggle" data-path="${attr(path)}" data-value="${selected ? "false" : "true"}"></button>`, false, hint);
}

function choices(path, label, values, selected, hint = "") {
  selected = currentValue(path, selected);
  return field(path, label, `<span class="choice-row">${values.map((value) => `<button type="button" class="${value === selected ? "selected" : ""}" data-action="config-choice" data-path="${attr(path)}" data-value="${attr(value)}">${html(value)}</button>`).join("")}</span>`, false, hint);
}

function selectSetting(path, label, options, selected) {
	selected = currentValue(path, selected);
	return field(path, label, `<select class="setting-input" data-path="${attr(path)}" data-kind="text">${options.map(([value, text]) => `<option value="${attr(value)}" ${value === selected ? "selected" : ""}>${html(text)}</option>`).join("")}</select>`);
}

function approvalChoices(selected, hint = "") {
  selected = currentValue("approval.mode", selected);
  const displayed = selected === "off" ? "boundary-only" : selected;
  const modes = [
    ["boundary-only", "Tools run without generic confirmation; Windows still gates anything outside your permissions."],
    ["mutating", "Confirm every file write, edit, and shell command."],
    ["all", "Confirm every tool call."],
  ];
  return field("approval.mode", "approval mode", `<span class="approval-choices">${modes.map(([value, explanation]) => `<button type="button" class="${value === displayed ? "selected" : ""}" data-action="config-choice" data-path="approval.mode" data-value="${attr(value)}"><strong>${html(value)}</strong><span>${html(explanation)}</span></button>`).join("")}</span>`, false, hint);
}

function copyRow(label, value, hint = "") {
  return row(label, `<span class="copy-value"><code title="${attr(value)}">${html(value || "—")}</code><button type="button" data-action="copy" data-value="${attr(value)}">copy</button></span>`, "", hint);
}

async function click(event) {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  const action = button.dataset.action;
  const id = button.dataset.id;
  if (action === "confirm-cancel") return void cancelConfirmation();
  if (action === "confirm-proceed") return void proceedWithConfirmation();
  // Item 2l4: a control that arms does not rewrite itself; it raises the popover.
  // Anything already confirming is answered, not re-armed.
  const armedBefore = armed.size;
  const wasPending = confirmPending;
  if (wasPending && wasPending.action === action && wasPending.id === (id || "")) confirmPending = null;
  const settle = () => {
    if (!wasPending && armed.size > armedBefore) {
      confirmPending = { action, id: id || "", question: removalQuestion(button), rect: rectOf(button) };
      render();
    }
  };
  try {
    const result = dispatchAction(event, button, action, id);
    if (result && typeof result.then === "function") await result;
  } finally {
    settle();
  }
  return;
}

// removalQuestion says what will be removed, in the words the control already uses.
function removalQuestion(button) {
  const label = button.dataset.confirm || button.getAttribute("aria-label") || button.title || button.textContent || "";
  return String(label).replace(/^Confirm\s+/i, "").trim() || "this";
}

function rectOf(button) {
  const box = button.getBoundingClientRect();
  return { top: box.bottom, left: box.left, right: box.right };
}

function cancelConfirmation() {
  if (!confirmPending) return;
  // (c): cancel leaves everything as it was, including the arming.
  armed.clear();
  confirmPending = null;
  render();
}

function proceedWithConfirmation() {
  const pending = confirmPending;
  if (!pending) return;
  const selector = `[data-action="${pending.action}"]${pending.id ? `[data-id="${CSS.escape(pending.id)}"]` : ""}`;
  const button = sheet.querySelector(selector);
  confirmPending = null;
  if (button) button.click();
  else { armed.clear(); render(); }
}

// The popover: what will be removed, confirm, cancel. Nothing else, no dimmed
// page, no dialog that takes over the view.
function confirmPopover() {
  if (!confirmPending) return "";
  const { rect, question } = confirmPending;
  return `<div class="confirm-popover" role="dialog" aria-modal="false" aria-label="Confirm" style="top:${Math.round(rect.top + 6)}px; left:${Math.round(Math.max(8, rect.right - 232))}px">
      <p>Remove ${html(question)}?</p>
      <div class="confirm-actions"><button type="button" data-action="confirm-cancel">Cancel</button><button type="button" class="confirm" data-action="confirm-proceed">Remove</button></div>
    </div>`;
}

// dispatchAction is the original body of click, unchanged.
async function dispatchAction(event, button, action, id) {
	if (action === "close") return void leaveSettingsForChat();
  // Item 2l6 (c): the save that belongs to one setting, beside it.
  if (action === "save-setting") return void applySetting(button.dataset.savePath);
  // Item 2l5 (d): the row save commits THAT connection pending changes and nothing
  // else. It is the explicit save 2l6 leaves in place for this surface.
  if (action === "save-connection") return void saveConnection(id);
  if (action === "settings-section") {
    activeSection = id;
    history.replaceState(null, "", `#settings/${activeSection}`);
    return render();
  }
  if (action === "connection-toggle") {
    const wasOpen = expanded.has(id);
    expanded.clear();
    if (!wasOpen) expanded.add(id);
    return render();
  }
  if (action === "show-key") {
    shownKeys.has(id) ? shownKeys.delete(id) : shownKeys.add(id);
    return render();
  }
  if (action === "save-settings") return saveSettings();
  if (action === "create-profile") {
    const name = sheet.querySelector("#new-profile-name")?.value || "";
    try { await api("/api/profiles", { action: "create", name }); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
    catch (error) { errors.set("profiles", error.message); }
    return render();
  }
  if (action === "switch-profile") {
    try { await api("/api/profiles", { action: "switch", name: id }); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
    catch (error) { errors.set("profiles", error.message); }
    return render();
  }
  if (action === "rename-profile") {
    const name = sheet.querySelector(`#rename-profile-${CSS.escape(id)}`)?.value || "";
    try { await api("/api/profiles", { action: "rename", name: id, new_name: name }); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
    catch (error) { errors.set("profiles", error.message); }
    return render();
  }
	if (action === "remove-agent-memory") {
		const key = `agent-memory:${id}`;
		if (!armed.has(key)) { armed.add(key); return render(); }
		armed.delete(key);
		const active = store.sessions[store.active];
		try { await api("/api/agent-memory/remove", { agent_id: active?.agent_id || "", note: id, confirm: true }); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
		catch (error) { errors.set("profiles", error.message); }
		return render();
	}
	if (action === "revoke-standing-grant") {
		const key=`standing-grant:${id}`; if(!armed.has(key)){armed.add(key);return render()} armed.delete(key);
		try { await api("/api/standing-grants",{id}); reduce({type:"snapshot",data:await api("/api/state",undefined,"GET")}); } catch(error) { errors.set("shell",error.message); render(); }
		return;
	}
  if (action === "operator-context") {
    try { await api("/api/config", {shell:{operator_context:!store.shell_identity?.operator_context}}); }
    catch (error) { errors.set("shell", error.message); render(); }
    return;
  }
	if (action === "local-network-toggle") {
		const enabled = button.getAttribute("aria-checked") !== "true";
		button.setAttribute("aria-checked", String(enabled));
		button.classList.toggle("on", enabled);
		for (const input of sheet.querySelectorAll("[data-local-subnet]")) input.disabled = !enabled;
		return;
	}
  if (action === "open-setup") { location.href = "/setup?from=settings"; return; }
  if (action === "config-toggle" || action === "config-choice") {
    const path = button.dataset.path;
    const value = action === "config-toggle" ? button.dataset.value === "true" : button.dataset.value;
    drafts.set(path, value);
    draftKinds.set(path, action === "config-toggle" ? "boolean" : "text");
    settingsSaveAlarm = false;
    // Item 2l6 (b): a toggle or a selection IS the commit — there is nothing more
    // to type, so it applies now.
    if (needsExplicitSave(path)) {
      settingsSaveMessage = "Unsaved — use the save beside the setting";
      return render();
    }
    render();
    return void applySetting(path);
  }
  if (action === "probe") {
    const pendingPrefix = `connections.${id}.`;
    const connection = connectionList().find((x) => x.id === id);
    if (connection) connection._probing = true;
    probeMessages.set(id, { message: "Testing…", alarm: false });
    render();
    try {
      const discovered = await api(`/api/connections/${encodeURIComponent(id)}/probe`, {
        base_url: current(`${pendingPrefix}base_url`, connection?.base_url || ""),
        model: current(`${pendingPrefix}model`, connection?.model || ""),
        api_key: current(`${pendingPrefix}api_key`, ""),
        request_timeout_s: Number(current(`${pendingPrefix}request_timeout_s`, connection?.request_timeout_s || 0)) || 0,
      });
      applyProposedValues(id, discovered);
      const needsModel = discovered.status === "model_required";
      if (discovered.changes?.base_url) {
        drafts.set(`${pendingPrefix}base_url`, discovered.changes.base_url);
        draftKinds.set(`${pendingPrefix}base_url`, "text");
        settingsSaveMessage = "Unsaved discovery change";
      }
      const observed = probeMessages.get(id) || {};
      const terminal = /^Test (?:passed|failed)/.test(observed.message || "");
      probeMessages.set(id, {
        ...discovered,
        found: discovered.message || "",
        message: terminal ? observed.message : (needsModel ? `Test failed — ${discovered.error}` : (discovered.message || "Testing…")),
        alarm: terminal ? !!observed.alarm : needsModel,
      });
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      render();
    } catch (error) {
      if (connection) connection._probing = false;
      errors.set(`connections.${id}`, error.message);
      probeMessages.set(id, { message: `Test failed — ${error.message}`, alarm: true });
      render();
    }
    return;
  }
  if (action === "measure-connection") {
    const currentState = await api(`/api/eval/measure?connection_id=${encodeURIComponent(id)}`, undefined, "GET").catch(() => ({ running: false }));
    try {
      if (currentState.running) await api(`/api/eval/measure?connection_id=${encodeURIComponent(id)}`, undefined, "DELETE");
      else await api("/api/eval/measure", { connection_id: id });
      probeMessages.set(id, { ...(probeMessages.get(id) || {}), measureRunning: !currentState.running, message: currentState.running ? "Evaluation Harness stopping" : "Evaluation Harness running", alarm: false });
      if (!currentState.running) void refreshMeasurement(id);
    } catch (error) { probeMessages.set(id, { ...(probeMessages.get(id) || {}), message: error.message, alarm: true }); }
    return render();
  }
  if (action === "add-connection") return addConnection();
  if (action === "duplicate-connection") return duplicateConnection(id);
  if (action === "remove-connection") return removeConnection(id);
  if (action === "new-session") return newSession();
  if (action === "close-session") return closeSession(id);
  if (action === "reset-session") return resetSession(id);
  if (action === "revoke-workspace-policy") {
		const key=`policy:${id}`; if(!armed.has(key)){armed.add(key);return render()} armed.delete(key);
		try{await api("/api/workspaces/policy-revoke",{dir:id});await refreshWorkspaceState();reduce({type:"snapshot",data:await api("/api/state",undefined,"GET")})}catch(error){errors.set("workspace",error.message);render()} return;
	}
  if (action === "empty-operator-attachments") {
	const key="operator-attachments:empty";
	if(!armed.has(key)){armed.add(key);return render()}
	armed.delete(key);
	try{await api("/api/operator-files",{action:"empty_attachments",confirm:true});await refreshOperatorFileState()}
	catch(error){errors.set("workspace",error.message);render()}
	return;
  }
  if (action === "adopt-instructions") {
	const cleanup=sheet.querySelector("#adopt-instruction-cleanup")?.checked===true;
	try{await api("/api/operator-files",{action:"adopt_instructions",dir:id,cleanup,confirm_cleanup:cleanup});await refreshOperatorFileState();await refreshWorkspaceState()}
	catch(error){errors.set("workspace",error.message);render()}
	return;
  }
  if (action === "session-tool-toggle") {
    const active = store.sessions[store.active];
    if (!active) return;
    try {
      await api(`/api/tools/${encodeURIComponent(id)}`, {
        session_id: active.id,
        enabled: button.dataset.enabled !== "true",
      });
    } catch (error) {
      errors.set(`tools.${id}`, error.message);
      render();
    }
    return;
  }
	if (action === "store-shell-credential") return storeShellCredential();
	if (action === "test-shell-credential") return shellCredentialAction("test");
	if (action === "clear-shell-credential") return shellCredentialAction("clear");
	if (action === "setup-service-account") return setupServiceAccount();
	if (action === "disable-service-account") {
		try {
			const result = await api("/api/config", {shell:{service_account:{enabled:false}}});
			reduce({ type: "config.changed", data: { config: result } });
			serviceAccountMessage = "service identity turned off";
			serviceAccountAlarm = false;
		} catch (error) {
			serviceAccountMessage = error.message;
			serviceAccountAlarm = true;
		}
		return render();
	}
	if (action === "refresh-service-account") return refreshServiceAccountStatus();
	if (action === "phone-enrol") {
		try { phoneAccess = { ...phoneAccess, ...await api("/api/phone/enrolment") }; } catch (error) { phoneAccess = { ...phoneAccess, error: error.message }; }
		return render();
	}
	if (action === "phone-revoke") {
		await api("/api/phone/devices/revoke", { id });
		return refreshPhoneAccess();
	}
	if (action === "phone-revoke-all") {
		await api("/api/phone/devices/revoke-all", {});
		return refreshPhoneAccess();
	}
	if (action === "phone-push-toggle") {
		try { const result = await api("/api/phone/push", { enabled: !phoneAccess.push_enabled }); phoneAccess = { ...phoneAccess, ...result }; }
		catch (error) { phoneAccess = { ...phoneAccess, error: error.message }; }
		return render();
	}
	if (action === "apply-hardening") return hardeningAction("apply");
	if (action === "verify-hardening") return hardeningAction("verify");
	if (action === "refresh-hardening") return refreshHardeningStatus();
	if (action === "save-notification") return notificationAction("save");
	if (action === "test-notification") return notificationAction("test");
	if (action === "clear-notification") return notificationAction("clear");
	if (action === "install-update") return installUpdate();
	if (action === "check-update") return checkUpdate();
	if (action === "remove-hardening") {
		if (!armed.has("hardening:remove")) {
			armed.add("hardening:remove");
			return render();
		}
		armed.delete("hardening:remove");
		return hardeningAction("remove");
	}
  if (action === "copy") {
    if (button.dataset.value) await navigator.clipboard?.writeText(button.dataset.value);
  }
}

async function refreshMeasurement(id) {
  for (let attempt = 0; attempt < 400; attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 750));
    const state = await api(`/api/eval/measure?connection_id=${encodeURIComponent(id)}`, undefined, "GET").catch(() => null);
    if (!state || state.running) continue;
    probeMessages.set(id, { ...(probeMessages.get(id) || {}), measureRunning: false, message: state.error || state.text || "Evaluation Harness complete", alarm: !!state.error });
    reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
    if (open && activeSection === "connections") render();
    return;
  }
}

async function refreshServiceAccountStatus(preserveMessage = false) {
	try {
		const status = await api("/api/service-account", undefined, "GET");
		serviceAccountStatus = { ...status, loaded: true };
		if (!preserveMessage) {
			serviceAccountMessage = "";
			serviceAccountAlarm = false;
		}
	} catch (error) {
		serviceAccountStatus = { loaded: true, supported: false, exists: false, administrator: false };
		serviceAccountMessage = error.message;
		serviceAccountAlarm = true;
	}
	if (open) render();
}

async function refreshPhoneAccess() {
	try { phoneAccess = { ...phoneAccess, ...await api("/api/phone/devices", undefined, "GET") }; }
	catch (error) { phoneAccess = { devices: [], error: error.message, push_enabled: false }; }
	if (open && activeSection === "shell") render();
}

function selectedHardeningConnectionID() {
	const ready = connectionList().filter((connection) => !connectionReason(connection));
	if (ready.some((connection) => connection.id === hardeningConnectionID)) return hardeningConnectionID;
	const activeID = store.sessions[store.active]?.connection_id;
	hardeningConnectionID = ready.some((connection) => connection.id === activeID) ? activeID : ready[0]?.id || "";
	return hardeningConnectionID;
}

async function refreshHardeningStatus(preserveMessage = false) {
	const connectionID = selectedHardeningConnectionID();
	if (!connectionID) {
		hardeningStatus = { loaded: true, supported: true, applied: false };
		hardeningMessage = "select a model connection before applying host protections";
		hardeningAlarm = true;
		if (open) render();
		return;
	}
	try {
		const status = await api(`/api/hardening?connection_id=${encodeURIComponent(connectionID)}`, undefined, "GET");
		hardeningStatus = { ...status, loaded: true };
		const operation = status.operation || {};
		hardeningBusy = operation.state === "running";
		if (operation.message && (!preserveMessage || operation.state === "running")) {
			hardeningMessage = operation.message;
			hardeningAlarm = operation.state === "failed";
		} else if (!preserveMessage) {
			hardeningMessage = "";
			hardeningAlarm = false;
		}
	} catch (error) {
		hardeningStatus = { loaded: true, supported: true, applied: false };
		hardeningMessage = error.message;
		hardeningAlarm = true;
	}
	if (open) render();
}

async function hardeningAction(action) {
	const connectionID = selectedHardeningConnectionID();
	if (!connectionID) {
		hardeningMessage = "Test and select a runnable model connection before changing host protections.";
		hardeningAlarm = true;
		return render();
	}
	hardeningBusy = true;
	hardeningAlarm = false;
	hardeningMessage = action === "verify"
		? "Verifying ACL and outbound policy…"
		: hardeningStatus.harness_elevated
			? "Agent_b is already elevated; applying directly without a UAC prompt."
			: "Windows elevation requested. Respond if a UAC prompt appears.";
	render();
	try {
		const lanSwitch = sheet.querySelector('[data-action="local-network-toggle"]');
		const allowLocalNetwork = action === "apply" ? lanSwitch?.getAttribute("aria-checked") === "true" : !!store.config.shell?.allow_local_network;
		const localSubnets = action === "apply" && allowLocalNetwork
			? [...sheet.querySelectorAll("[data-local-subnet]:checked")].map((input) => input.value)
			: store.config.shell?.confirmed_local_subnets || [];
		const result = await api("/api/hardening", { action, connection_id: connectionID, allow_local_network: allowLocalNetwork, local_subnets: localSubnets });
		hardeningStatus = { ...(result.status || hardeningStatus), loaded: true };
		if (action === "apply" && result.ok !== false) {
			store.config.shell.allow_local_network = allowLocalNetwork;
			store.config.shell.confirmed_local_subnets = allowLocalNetwork ? localSubnets : [];
		}
		if (result.operation) hardeningStatus.operation = result.operation;
		hardeningMessage = result.operation?.message || result.message;
		hardeningAlarm = result.ok === false || result.operation?.state === "failed" || (action === "apply" && !hardeningStatus.applied);
	} catch (error) {
		hardeningMessage = error.message;
		hardeningAlarm = true;
		await refreshHardeningStatus(true);
	} finally {
		hardeningBusy = hardeningStatus.operation?.state === "running";
		if (open) render();
	}
}

async function refreshNotificationStatus() {
	try {
		notificationStatus = await api("/api/notifications", undefined, "GET");
	} catch (error) {
		notificationStatus = { configured: false, host: "" };
		notificationMessage = error.message;
		notificationAlarm = true;
	}
	if (open && activeSection === "notifications") render();
}

async function notificationAction(action) {
	const input = sheet.querySelector("#discord-webhook-url");
	const value = input?.value || "";
	notificationBusy = true;
	notificationAlarm = false;
	notificationMessage = action === "test" ? "Sending test…" : "Saving…";
	if (input) input.value = "";
	render();
	try {
		const result = await api("/api/notifications", action === "save" ? { action, url: value } : { action });
		notificationStatus = result.notifications || notificationStatus;
		notificationMessage = action === "test" ? "Test sent." : action === "clear" ? "Discord notifications disabled." : "Discord webhook saved.";
	} catch (error) {
		notificationMessage = error.message;
		notificationAlarm = true;
	} finally {
		notificationBusy = false;
		if (open) render();
	}
}

async function installUpdate() {
	store.update = { ...(store.update || {}), installing: true, error: "" };
	render();
	try {
		const result = await api("/api/update", { action: "install", session_id: store.selection?.session_id || "" });
		store.update = result.update || store.update;
	} catch (error) {
		store.update = { ...(store.update || {}), installing: false, error: error.message };
	}
	if (open) render();
}

async function checkUpdate() {
	store.update = { ...(store.update || {}), checking: true, error: "" };
	render();
	try {
		store.update = await api("/api/update", { action: "check" });
	} catch (error) {
		store.update = { ...(store.update || {}), checking: false, error: error.message };
	}
	if (open) render();
}

async function setupServiceAccount() {
	const connectionID = selectedHardeningConnectionID();
	if (!connectionID) {
		serviceAccountMessage = "Test and select a runnable model connection first.";
		serviceAccountAlarm = true;
		return render();
	}
	serviceAccountBusy = true;
	serviceAccountAlarm = false;
	serviceAccountMessage = "Agent_b sets up its service identity now; Windows will ask once";
	render();
	try {
		const lanSwitch = sheet.querySelector('[data-action="local-network-toggle"]');
		const allowLocalNetwork = lanSwitch?.getAttribute("aria-checked") === "true";
		const localSubnets = allowLocalNetwork ? [...sheet.querySelectorAll("[data-local-subnet]:checked")].map((input) => input.value) : [];
		const result = await api("/api/service-account", { action: "provision", connection_id: connectionID, allow_local_network: allowLocalNetwork, local_subnets: localSubnets });
		serviceAccountStatus = { ...(result.account || serviceAccountStatus), loaded: true };
		store.shell_credential = result.credential || store.shell_credential;
		store.shell_identity = result.identity || store.shell_identity;
		if (result.config) reduce({ type: "config.changed", data: { config: result.config } });
		serviceAccountMessage = result.message;
		serviceAccountAlarm = !result.ok;
		await refreshHardeningStatus();
	} catch (error) {
		if (error.data?.credential) store.shell_credential = error.data.credential;
		serviceAccountMessage = error.message;
		serviceAccountAlarm = true;
		await refreshServiceAccountStatus(true);
	} finally {
		serviceAccountBusy = false;
		if (open) render();
	}
}

async function storeShellCredential() {
	const input = sheet.querySelector("#shell-service-password");
	const password = input?.value || "";
	if (input) input.value = "";
	try {
		const status = await api("/api/shell-credential", { action: "store", password });
		store.shell_credential = status;
		shellCredentialMessage = "credential stored";
		shellCredentialAlarm = false;
	} catch (error) {
		shellCredentialMessage = error.message;
		shellCredentialAlarm = true;
	}
	render();
}

async function shellCredentialAction(action) {
	try {
		const result = await api("/api/shell-credential", { action });
		if (action === "clear") {
			store.shell_credential = result;
			shellCredentialMessage = "credential removed";
		} else {
			store.shell_credential = result.credential || store.shell_credential;
			store.shell_identity = result.identity || store.shell_identity;
			serviceAccountMessage = result.message;
			serviceAccountAlarm = false;
		}
		shellCredentialAlarm = false;
	} catch (error) {
		if (action === "test") {
			serviceAccountMessage = error.message;
			serviceAccountAlarm = true;
		} else {
			shellCredentialMessage = error.message;
			shellCredentialAlarm = true;
		}
	}
	render();
}

async function blur(event) {
  const input = event.target;
  if (input.matches(".setting-input[data-path]")) {
    const path = input.dataset.path;
    if (input.dataset.kind === "secret" && input.value === "•••• set") return;
    drafts.set(path, input.value);
    draftKinds.set(path, input.dataset.kind || "text");
    settingsSaveAlarm = false;
    if (needsExplicitSave(path)) {
      settingsSaveMessage = "Unsaved — use the save beside the setting";
      refreshSaveControls();
    } else await applySetting(path);
  }
  if (input.matches("[data-session-label]")) {
    try {
      await api(`/api/sessions/${encodeURIComponent(input.dataset.sessionLabel)}`, { label: input.value });
    } catch (error) {
      errors.set(`session.${input.dataset.sessionLabel}`, error.message);
      render();
    }
  }
}

async function change(event) {
  if (event.target.matches("#hardening-connection")) {
    hardeningConnectionID = event.target.value;
    hardeningStatus = { loaded: false, supported: true, applied: false };
    hardeningMessage = "";
    render();
    await refreshHardeningStatus();
    return;
  }
  const select = event.target.closest("[data-session-connection]");
  if (!select) return;
  const id = select.dataset.sessionServer;
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}`, { connection_id: select.value });
    errors.delete(`session.${id}`);
    reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
    setActive(id);
  } catch (error) {
    errors.set(`session.${id}`, error.message);
    render();
  }
}

function draftValue(raw, kind = "text") {
  if (kind === "boolean") return raw === true || raw === "true";
  if (kind === "number") return Number(raw);
  if (kind === "percent") return Number(raw) / 100;
  if (kind === "list") return String(raw).split(",").map((value) => value.trim()).filter(Boolean);
  if (kind === "command") return String(raw).trim().split(/\s+/).filter(Boolean);
  return raw;
}

function combinedPatch(entries) {
  const result = {};
  const connections = new Map();
  for (const [path, raw] of entries) {
    const parts = path.split(".");
    const value = draftValue(raw, draftKinds.get(path));
    if (parts[0] === "connections") {
      const item = connections.get(parts[1]) || { id: parts[1] };
      assign(item, parts.slice(2), value);
      connections.set(parts[1], item);
    } else assign(result, parts, value);
  }
  if (connections.size) result.connections = [...connections.values()];
  return result;
}

async function saveSettings(pathPrefix = "") {
  if (settingsSaving) return false;
  const entries = [...drafts.entries()].filter(([path]) => !pathPrefix || path.startsWith(pathPrefix));
  if (!entries.length) return true;
  const changedPaths = entries.map(([path]) => path);
  settingsSaving = true;
  settingsSaveMessage = "Saving changes…";
  settingsSaveAlarm = false;
  refreshSaveControls();
  try {
    const result = await api("/api/config", combinedPatch(entries));
    for (const path of changedPaths) {
      drafts.delete(path);
      draftKinds.delete(path);
      errors.delete(path);
    }
    reduce({ type: "config.changed", data: { config: result } });
    settingsSaveMessage = drafts.size ? `${drafts.size} unsaved change${drafts.size === 1 ? "" : "s"} remain` : "All changes saved";
    if (changedPaths.some((path) => path === "shell.service_account.account" || path === "shell.service_account.domain"))
      await refreshServiceAccountStatus();
  } catch (error) {
    errors.set(error.field || "config", error.message);
    settingsSaveMessage = `Save failed: ${error.message}`;
    settingsSaveAlarm = true;
    return false;
  } finally {
    settingsSaving = false;
    if (open) render();
  }
  return true;
}

function assign(target, parts, value) {
  let node = target;
  parts.forEach((part, index) => {
    if (index === parts.length - 1) node[part] = value;
    else node = node[part] ||= {};
  });
}

async function addConnection() {
  const id = uniqueID("server");
  expanded.clear();
  expanded.add(id);
  try {
    const result = await api("/api/config", { connections: [{ id, label: id, base_url: "http://127.0.0.1:8000", model: "model" }] });
    probeMessages.set(id, { message: "Added and saved — edit, then Test", alarm: false });
    reduce({ type: "config.changed", data: { config: result } });
  } catch (error) {
    expanded.delete(id);
    errors.set("connections", error.message);
    settingsSaveMessage = `Add failed: ${error.message}`;
    settingsSaveAlarm = true;
    render();
  }
}

async function duplicateConnection(id) {
  const source = connectionList().find((x) => x.id === id);
  if (!source) return;
  const copy = structuredClone(source);
  delete copy._probing;
  copy.id = uniqueID(`${id}-2`);
  copy.label = `${source.label} copy`;
  if (copy.api_key === "•••• set") {
    copy.api_key = "";
    copy.credential = "";
  }
  expanded.clear();
  expanded.add(copy.id);
  const result = await api("/api/config", { connections: [copy] });
  reduce({ type: "config.changed", data: { config: result } });
}

function uniqueID(base) {
  let id = base;
  let suffix = 2;
  while (connectionList().some((connection) => connection.id === id)) id = `${base}-${suffix++}`;
  return id;
}

async function removeConnection(id) {
  const key = `connection:${id}`;
  if (!armed.has(key)) {
    armed.add(key);
    return render();
  }
  try {
    await api(`/api/connections/${encodeURIComponent(id)}`, undefined, "DELETE");
    armed.delete(key);
  } catch (error) {
    errors.set(`connections.${id}`, error.message);
    armed.delete(key);
    render();
  }
}

async function newSession() {
  const body = {
    label: sheet.querySelector("#new-session-label").value,
    connection_id: sheet.querySelector("#new-session-connection").value,
  };
  try {
    const result = await api("/api/sessions", body);
    reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
    setActive(result.session.id);
  } catch (error) {
    errors.set("new-session", error.message);
    render();
  }
}

async function closeSession(id) {
  const item = store.sessions[id];
  const key = `session:${id}`;
  if (item?.run.status !== "idle" && !armed.has(key)) {
    armed.add(key);
    return render();
  }
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}${armed.has(key) ? "?force=1" : ""}`, undefined, "DELETE");
    armed.delete(key);
  } catch (error) {
    errors.set(`session.${id}`, error.message);
    render();
  }
}

async function resetSession(id) {
  const key = `reset:${id}`;
  if (!armed.has(key)) {
    armed.add(key);
    return render();
  }
  try {
    await api(`/api/sessions/${encodeURIComponent(id)}/reset${store.sessions[id]?.run.status === "idle" ? "" : "?force=1"}`, {});
    armed.delete(key);
  } catch (error) {
    errors.set(`session.${id}`, error.message);
    render();
  }
}

function connectionReason(connection) {
  const caps = connection.capabilities || {};
  const nctx = connection.context?.n_ctx;
  if (!nctx) return "context length unknown";
  if (!caps.tool_calls) return "tool calling unavailable";
  if (caps.overflow_behavior === "truncate") return "server truncates context";
  if (!caps.streaming) return "streaming unavailable";
  if (store.config.context?.accounting === "exact" && !caps.tokenize)
    return "exact accounting requested but this server has no /tokenize";
  return "";
}

function html(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[ch]);
}
function attr(value) {
  return html(value);
}
