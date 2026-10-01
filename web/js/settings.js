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
import { setWaitProgress, waitElement } from "./wait.js";
import { agentKey } from "./panel-lifetime.js";
import { mountPanels, unmountPanels } from "./app.js";
import { mountPlan, unmountPlan } from "./plan.js";

const sheet = document.getElementById("settings-page");
let gear;
const expanded = new Set();
const advancedConnections = new Set();
const armed = new Set();
const drafts = new Map();
const draftKinds = new Map();
const errors = new Map();
// Item 2nb (c): the connections whose model the operator chose to type by hand. Not a
// draft, because a draft is a configuration path and this is a choice about the
// control rather than a value to save.
const typedModels = new Set();
const probeMessages = new Map();
const shownKeys = new Set();
let open = false;
let lastFocus = null;
let shellCredentialMessage = "";
let shellCredentialAlarm = false;
let serviceAccountStatus = { loaded: false, supported: true, exists: false, administrator: false };
let serviceAccountBusy = false;
let serviceAccountMessage = "";
let serviceAccountSteps = [];
// Item 2np (b): the log path is SECONDARY TEXT under the one sentence, not a second
// message and not a paragraph of its own.
let serviceAccountLog = "";
// Item 2kq (b) and (e): the broker's own state, refreshed while the sheet is open.
let brokerStatus = {};
// Item 2nv (c): the credential listing. It carries names, origins and dates and never a
// value, because the endpoint that answers it has none to give.
let credentialList = [];
let credentialMessage = "";
let credentialAlarm = false;
let credentialDevice = {};
let brokerMessage = "";
let brokerAlarm = false;
let serviceAccountAlarm = false;
let hardeningStatus = { loaded: false, supported: true, applied: false };
let hardeningBusy = false;
let hardeningMessage = "";
let hardeningAlarm = false;
let notificationStatus = { configured: false, host: "" };
let notificationBusy = false;
let notificationMessage = "";
let notificationAlarm = false, signInStart = { loaded: false, enabled: false, busy: false, error: "" };
let settingsSaving = false;
let settingsSaveMessage = "All changes saved";
let settingsSaveAlarm = false;
let activeSection = "connections";
// The view another document was on when it sent us here; "" when the sheet was
// opened from inside this one, where it closes onto the chat beneath it.
let openedFrom = "";
// Item 2no (d) and (e): true when this document was opened AT the Plan — /plan,
// ?from=plan, or #settings/plan.
let planRequestedByAddress = false;
let hardeningConnectionID = "";
let workspaceState = [];
let operatorFileState = { attachment_files: 0, attachment_bytes: 0, instruction_found: [] };
let phoneAccess = { devices: [], code: "", expires_at: "", push_enabled: false };
const connectionList = () => Array.isArray(store.connections) ? store.connections : [];

// Item 2gk: Agents and Activity are where the page that used to stand on its
// own now lives. They come first because they are what the operator opened
// that page to read.
// Item 2ni (a): and the Plan is one of them, its own top-level entry, third:
// "why is plan a chat tab on the left side? ... i decided i think i want it under
// settings, as its own top level item please implement." It comes after the two
// the operator opened this sheet to read and before the machine settings, which is
// where a document he writes in belongs.
const sectionLabels = [
  ["agents", "Agents"],
  ["activity", "Activity"],
  ["plan", "Plan"],
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
    if (event.target.id && event.target.matches('input:not([type="password"]):not(.setting-input[data-path]), textarea:not(.setting-input[data-path])')) {
      actionDrafts.set(event.target.id, event.target.value);
    }
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
    // Item 2nb (b) and (i): the walk reports each address as it lands, and the sheet
    // shows it in the one waiting element, determinate: addresses tried of the total,
    // with the line naming the address in hand and what it found.
    if (event.type === "connection.discovering") {
      const connectionID = event.data?.connection_id || "";
      probeMessages.set(connectionID, {
        ...(probeMessages.get(connectionID) || {}),
        // Item 2nn (c): the line names the PHASE, not the raw verdict. One step per
        // address when no port was typed, counted, so the bar is determinate.
        walking: {
          line: probePhaseLine(event.data),
          processed: Number(event.data?.tried) || 0,
          total: Number(event.data?.total) || 0,
        },
      });
      // The bar is moved in place rather than by a render: "connection.discovering"
      // is not in the render list below, and a full render of the sheet mid-walk
      // would take the focus out of the field he was typing in.
      const moved = probeMessages.get(connectionID).walking;
      for (const bar of sheet.querySelectorAll(`[data-probe-wait="${CSS.escape(connectionID)}"] .wait`)) {
        bar.querySelector(".wait-line").textContent = moved.line;
        setWaitProgress(bar, moved);
      }
    }
    if (event.type === "connection.probed") {
      const connectionID = event.data?.connection_id || "";
      const findings = event.data?.capabilities?.findings || event.data?.findings || [];
      const failed = findings.find((value) => String(value).startsWith("probe failed:"));
      probeMessages.set(connectionID, { ...(probeMessages.get(connectionID) || {}), walking: null, message: failed ? `Test failed — ${String(failed).slice(13).trim()}` : "Test passed", alarm: !!failed });
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
  if (section === "plan") planRequestedByAddress = true;
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
	refreshNotificationStatus(); refreshSignInStart();
	refreshPhoneAccess();
  refreshWorkspaceState();
  refreshOperatorFileState();
  void refreshBrokerStatus().then(() => { if (open) render(); });
  watchBrokerPairing();
  void refreshCredentials().then(() => { if (open) render(); });
  requestAnimationFrame(() => sheet.querySelector(".settings-nav button.selected")?.focus());
  recordViewMount("settings", performance.now() - started);
}

export function closeSettings(surface = "chat") {
  if (!open) return;
  open = false;
  if (brokerWatch) {
    clearInterval(brokerWatch);
    brokerWatch = null;
  }
  // The panels go home before the sheet is hidden, so they are never left
  // inside a hidden surface where the next render would wipe them.
  returnAdoptedPanels();
  unmountPanels();
  // Item 2no (b): closing the sheet is leaving the section, so the Plan stops too.
  unmountPlan();
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
  // Item 2no (d): there is no Plan document to go back to any more — `from=plan` and
  // `/plan` both land on this sheet's Plan section, and closing it returns to the chat
  // beneath like every other section.
  openedFrom = "";
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
    // Item 2ni (a) and (b): the Plan page is its own document, so the entry NAVIGATES
    // there and this body is only what is shown when it cannot. The availability rule
    // that used to decide whether the tab existed decides that and nothing else: the
    // entry is always here.
    plan: () => planSection(),
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
        ${navSections().map(([id, name]) => `<button type="button" class="${id === activeSection ? "selected" : ""}" data-action="settings-section" data-id="${id}" aria-current="${id === activeSection ? "page" : "false"}">${sectionChip(id)}${name}</button>`).join("")}
      </nav>
      <div class="settings-content" tabindex="-1">${group(label, content[activeSection](), activeSection)}${confirmPopover()}</div>
    </div>`;
  adoptPanels();
  const contentNode = sheet.querySelector(".settings-content");
  contentNode.scrollTop = scrollTop;
  for (const input of sheet.querySelectorAll('input[type="password"]')) {
    const value = secretValues.get(controlKey(input));
    if (value) input.value = value;
  }
  for (const [id, value] of actionDrafts) {
    const input = sheet.querySelector(`#${CSS.escape(id)}`);
    if (input) input.value = value;
  }
  const focusNode = [...sheet.querySelectorAll("button, input, textarea, select, summary")]
    .find((node) => controlKey(node) === focusKey);
  focusNode?.focus({ preventScroll: true });
  // Item 2nn (c): THE BAR, NOT THE WORD. Test showed the bare text "testing" on the
  // control and, unless the walk happened to emit events, nothing else — "there are
  // still no loading bars. i hit test and it just says testing and goes back to that
  // port". The element goes INSIDE the control that was pressed, with the phase line,
  // and is gone when the result renders.
  for (const seat of sheet.querySelectorAll("[data-probe-wait]")) {
    const walking = probeMessages.get(seat.dataset.probeWait)?.walking;
    if (!walking) continue;
    seat.replaceChildren(waitElement(document, { line: walking.line, processed: walking.processed, total: walking.total }));
  }
  // The Evaluation Harness runs a known ten briefs per arm, so its bar is
  // determinate and its line is the count with the arm named — the mode
  // @consequence-if-false allows for is not needed, because the server counts.
  for (const seat of sheet.querySelectorAll("[data-harness-wait]")) {
    const walking = probeMessages.get(seat.dataset.harnessWait)?.harnessWalking;
    if (!walking) continue;
    seat.replaceChildren(waitElement(document, { line: walking.line, processed: walking.processed, total: walking.total }));
  }
  // Item 2nh (a): the same element in the About page's update row, determinate while
  // the download is the stage in hand — the release manifest names the size, so the
  // bytes are real — and indeterminate for every stage that reports no fraction.
  for (const seat of sheet.querySelectorAll("[data-update-wait]")) {
    const total = Number(seat.dataset.updateTotal || 0);
    const processed = Number(seat.dataset.updateProcessed || 0);
    seat.replaceChildren(waitElement(document, {
      line: seat.dataset.updateLine || "starting the update",
      processed: total > 0 ? processed : null,
      total: total > 0 ? total : null,
    }));
  }
  // Item 2nn (b): a Save control is only ever as live as the drafts, and the
  // markup is written before this render knows them, so the same refresh that
  // typing calls runs here too.
  refreshSaveControls();
  placeConfirmPopover();
  navigationSurfaceReady("settings", store);
}

// Item 2nc (c), corrected at rel-1.31.0 by item 2mo's journey walking it: the rect
// was captured AT THE CLICK and the render that follows moves the page — the armed
// row grows, a refusal appears — so on a scrolled sheet the question opened 400px
// below the control it belonged to and off the bottom of the screen. Measured:
// control at 462, popover at 870, not visible.
//
// The position is taken AFTER the render, from the control's own live rectangle, in
// the scroller's coordinates. Nothing is captured that a layout can invalidate.
function placeConfirmPopover() {
  const popover = sheet.querySelector(".confirm-popover");
  if (!popover || !confirmPending) return;
  const selector = `[data-action="${confirmPending.action}"]${confirmPending.id ? `[data-id="${CSS.escape(confirmPending.id)}"]` : ""}`;
  const control = sheet.querySelector(selector);
  const scroller = sheet.querySelector(".settings-content");
  if (!control || !scroller) return;
  const box = control.getBoundingClientRect();
  const host = scroller.getBoundingClientRect();
  const top = box.bottom - host.top + scroller.scrollTop + 6;
  const left = box.right - host.left + scroller.scrollLeft - POPOVER_WIDTH;
  popover.style.top = `${Math.round(Math.max(0, top))}px`;
  popover.style.left = `${Math.round(Math.max(8, left))}px`;
}

// Item 2gk: the two panels are MOVED between their source holder and the open
// sheet. Moving rather than copying is the whole point — the nodes carry
// the listeners app.js set, and the renderers write into them by id, so a rebuilt
// copy would be a second, dead set of controls.
function setSheetStyles(on) {
  // Item 2no: plan.css dresses the Plan section now, so it is switched on with the
  // sheet beside app.css rather than linked by a document of its own.
  for (const id of ["panel-styles", "plan-styles"]) {
    const styles = document.getElementById(id);
    if (styles) styles.disabled = !on;
  }
}

function returnAdoptedPanels() {
  const sources = document.getElementById("panel-sources");
  if (!sources) return;
  for (const panel of sheet.querySelectorAll("#agents-panel, #activity-panel, #plan-panel")) sources.append(panel);
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
  if (sheet.querySelector("#agents-panel, #activity-panel")) mountPanels();
  else unmountPanels();
  // Item 2no (b): the Plan keeps its own state the way the other sections keep
  // theirs — its subscriber and its timer run while it is on screen and stop when it
  // is not.
  if (sheet.querySelector("#plan-panel")) mountPlan();
  else unmountPlan();
  void adopted;
}

function settingsPageContext(active) {
  return {
    active, store, expanded, advancedConnections, armed, drafts, errors, probeMessages, typedModels, workspaceState, operatorFileState, phoneAccess, standingGrants: store.standing_grants || [], hermesPreview, hermesReport, actionDrafts,
    shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountLog, serviceAccountSteps,
    brokerStatus, brokerMessage, brokerAlarm,
    credentialList, credentialMessage, credentialAlarm, credentialDevice,
    serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage,
    hardeningAlarm, connectionList,
    notificationStatus, notificationBusy, notificationMessage, notificationAlarm, signInStart,
    row, subhead, field, text, number, numberControl, textarea, secret, toggle, choices, selectSetting, approvalChoices,
    copyRow, currentValue, issue, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView,
  };
}

// Item 2l6 (d): there is no sheet-wide Save to refresh any more. The status line
// is what remains, and it says what the last commit did.
// Item 2nn (b): SAVE IS SAVE.
//
// "i went to rename it and couldn't save it after a rename. guessing i have to test
// first then it saves that port — i DON'T WANT THAT." Reproduced on a disposable root
// with the shipped v1.31.0: after typing, the status line said "Unsaved" and the row's
// Save was DISABLED, so the click did nothing and NO request was ever made. Nothing
// refused him — the button was dead.
//
// The cause was here: this function updated the status TEXT and nothing else, while
// every Save control carried the disabled attribute from the last render, computed
// from the drafts as they were THEN. A Test re-rendered the sheet, which is why
// testing first appeared to be the price of saving. The controls are part of the
// state this refreshes now.
function refreshSaveControls() {
  const status = sheet.querySelector("[data-save-status]");
  if (status) {
    status.textContent = settingsSaveMessage;
    status.classList.toggle("alarm", settingsSaveAlarm);
  }
  for (const control of sheet.querySelectorAll('[data-action="save-connection"][data-id]')) {
    const prefix = `connections.${control.dataset.id}.`;
    control.disabled = ![...drafts.keys()].some((path) => path.startsWith(prefix));
  }
  for (const control of sheet.querySelectorAll("[data-action=\"save-setting\"][data-save-path]")) {
    control.disabled = !drafts.has(control.dataset.savePath);
  }
}
function controlKey(node) {
  if (!node || !sheet.contains(node)) return "";
  return node.id || node.dataset?.path || [node.dataset?.action, node.dataset?.id || node.dataset?.setupAction].filter(Boolean).join(":") || node.getAttribute?.("aria-label") || "";
}

// Item 2ly (c): the surface says WHICH SCOPE it is, once per group rather than
// once per row — an operator must never have to guess whether a change follows
// them to another profile. A machine-wide page is NOT marked: the absence is the
// statement, and marking both would be noise on every row of every page.
//
// The classification itself lives in Go (internal/config/scopes.go) and this is
// the list of NAV SECTIONS whose page edits one of those keys. A section that
// moves scope moves in both places, and settings-scope.test.mjs derives the
// second list from the first and fails when they disagree.
//
// These three, and why: Chats carries chat, run, context and reflection (it
// composes the Run and Context pages into itself); Agents carries agents;
// Notifications carries notifications. The first list rel-1.20.0 shipped named
// "sessions" and "memory" instead -- two entries of the content map that are NOT
// in sectionLabels and so cannot be the active section at all -- and left Agents
// and Notifications, which an operator reaches from the nav, saying nothing. The
// test was written second and found it.
export const perProfileSections = new Set(["chats", "agents", "notifications"]);

// Item 2ni (b): the AVAILABILITY RULE, moved with the Plan and not widened. It is
// the rule the tab carried: the Plan is offered on a planner chat, or when no
// separate d connection is configured at all. It decides what the section SHOWS.
// probePhaseLine turns one discovery attempt into the phase the operator is waiting
// through, in item 2nn (c)'s words: connecting, then listing, then reading. The raw
// verdict stays in the note the result renders; a bar says what is happening NOW.
function probePhaseLine(data) {
  const address = data?.base_url || "the address";
  const result = String(data?.result || "");
  if (/model list answered/.test(result)) return `listing models at ${address}`;
  if (/wants an API key/.test(result)) return `${address} wants an API key`;
  if (/no model API/.test(result)) return `looking for the model list at ${address}`;
  return `connecting to ${address}`;
}

function planAvailable() {
  const session = store.sessions?.[store.selection?.session_id || ""];
  if (!session) return false;
  const configured = (store.config.agents || []).find((agent) => agentKey(agent) === session.agent_id) || store.config.agents?.[0];
  return session.role === "d" || !String(configured?.d || "").trim();
}

// Items 2le and 2lm, kept by 2ni: THE OPERATOR'S OWN PREPARED ARTWORK STAYS, and it
// moves with the Plan. "i want this to be the new plan icon … maintain the color of
// the icon itself" — the same three files at the same drawn 12px, beside the nav
// entry now that the tab it sat in is gone. The size set is not optional: his own
// display renders that box at 1.75x, so the browser picks.
function sectionChip(id) {
  if (id !== "plan") return "";
  return '<img class="shell-page-chip" src="/static/assets/plan-mark-nav.png" srcset="/static/assets/plan-mark-nav.png 1x, /static/assets/plan-mark-nav@2x.png 2x, /static/assets/plan-mark-nav@3x.png 3x" width="12" height="12" alt="" decoding="async">';
}

function planSection() {
  // Item 2no (a) and (b): the Plan is DRAWN HERE, in the pane, exactly as Agents and
  // Activity are — the same adoption, the same container, the same nav selection. It
  // used to navigate to a document of its own, which is the transition he watched
  // glitch.
  // (e): the entry exists either way; this is the one line that says why the document
  // is not here, in the words of the thing to do about it.
  // The address asking for the Plan is the operator asking for it. Before item 2no
  // the rule gated the nav ENTRY's content and never the document: /plan rendered the
  // Plan whatever chat was selected. That stays true — what the rule still decides is
  // what the entry shows when he arrives at it from the nav on a chat that cannot
  // plan (item 2ni (b), kept by 2no (e)).
  if (planAvailable() || planRequestedByAddress) return '<div data-adopt="plan-panel"></div>';
  return `<p class="settings-plan-note">Assign a planner in Agents to write a plan. The Plan opens on a planner chat, or on any chat when one connection serves every role.</p>`;
}

// Item 2ni: hiding the Plan hides its ENTRY, which is the operator's own choice
// from Settings > Chats. The availability rule never removes an entry (2ni (b));
// only this does.
function navSections() {
  const hidden = new Set(Array.isArray(store.config.chat?.hidden_surfaces) ? store.config.chat.hidden_surfaces : []);
  return sectionLabels.filter(([id]) => !(id === "plan" && hidden.has("plan")));
}

function group(name, content, section = "") {
  const scoped = perProfileSections.has(section)
    ? `<p class="settings-scope-note">These apply to the <strong>${html(store.profiles?.active || store.config.profiles?.active || "current")}</strong> profile. Another profile keeps its own.</p>`
    : "";
  return `<section class="settings-group"><h2>${name}</h2>${scoped}${content}</section>`;
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
// Item 2nb (d): the label follows the model when the operator has not named the
// connection himself (which is item 2mh (a)'s rule), and the values Test learned are
// re-proposed for the model now chosen.
function fillFromPickedModel(id, model) {
  const connection = connectionList().find((x) => x.id === id);
  if (!connection) return;
  const prefix = `connections.${id}.`;
  const displayName = String(model).split(/[\\/]/).pop();
  // Untouched means: never edited in this session, and still the name the Add button
  // generated. A label the operator chose is never overwritten.
  const untouched = !drafts.has(prefix + "label") && (!connection.label || connection.label === connection.id);
  if (untouched && displayName) {
    drafts.set(prefix + "label", displayName);
    draftKinds.set(prefix + "label", "text");
  }
  applyProposedValues(id, probeMessages.get(id));
  render();
}

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
  // Item 2mh (a): and the NAME, when the operator has not given one. "server-2"
  // identifies nothing; the model it serves does. The same untouched rule as
  // fillFromPickedModel — never typed here, and still the id the Add button
  // generated — so pressing Test again never overwrites a name he chose.
  const labelUntouched = !drafts.has(prefix + "label") && (!connection.label || connection.label === connection.id);
  if (labelUntouched && proposed.label) {
    drafts.set(prefix + "label", proposed.label);
    draftKinds.set(prefix + "label", "text");
    proposedFields.add(prefix + "label");
  }
  if (proposedFields.size) settingsSaveMessage = "Proposed values are unsaved — review and Save";
}

const proposedFields = new Set();
// Item 2l6 (b): which rows have just taken effect, so the row can say so.
const appliedSettings = new Map();
const actionDrafts = new Map();
let hermesPreview = null;
let hermesReport = null;
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

// Item 2nb (f): THE KEY IS NEVER TEXT ON THE PAGE. The field used to be rendered with
// the server's mask as its VALUE, so the placeholder for a stored key was a string in
// an input that could be submitted, shown, copied or half-edited. The field is empty
// now, a note beside it says a key is stored, and what "show" reveals is only what was
// typed this session, because that is the only thing the field ever holds.
function secret(path, label, value, id, hint = "") {
  const shown = shownKeys.has(id);
  const type = shown ? "text" : "password";
  const stored = typeof value === "string" && value.includes("••••");
  const typedThisSession = drafts.has(path) ? String(drafts.get(path)) : "";
  const note = stored
    ? `<span class="control-note">${typedThisSession ? "replacing the stored key" : "stored"}</span>`
    : "";
  return field(path, label, `<span class="secret-control"><input class="setting-input" type="${type}" data-path="${attr(path)}" data-kind="secret" value="${attr(typedThisSession)}" placeholder="${attr(stored ? "leave empty to keep the stored key" : "paste the API key")}"><button type="button" data-action="show-key" data-id="${attr(id)}">${shown ? "hide" : "show"}</button>${note}</span>`, false, hint);
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

// Item 2nc (c): THE POPOVER ANCHORS TO ITS CONTROL, in the settings scroller's own
// coordinate space rather than the viewport's. It was fixed to the viewport and its
// position captured once, so scrolling the sheet left the question floating away from
// the control that raised it. The rect is the scroller's content space now, and it is
// clamped so the popover cannot land outside the sheet.
const POPOVER_WIDTH = 232;
function rectOf(button) {
  const box = button.getBoundingClientRect();
  const scroller = sheet.querySelector(".settings-content");
  if (!scroller) return { top: box.bottom, left: box.left, right: box.right };
  const host = scroller.getBoundingClientRect();
  const top = box.bottom - host.top + scroller.scrollTop;
  const right = box.right - host.left + scroller.scrollLeft;
  const widest = Math.max(POPOVER_WIDTH + 16, scroller.scrollWidth);
  return {
    top: Math.max(0, top),
    left: box.left - host.left + scroller.scrollLeft,
    right: Math.min(right, widest - 8),
  };
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
	if (action === "delete-all-chats") {
		const count = Object.keys(store.sessions || {}).length;
		if (!confirm(`Delete all ${count} chat${count === 1 ? "" : "s"}? Memory notes, plans and files stay.`)) return;
		try { await api("/api/chats/delete-all", { confirm: true }); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); leaveSettingsForChat(); }
		catch (error) { errors.set("chat_root", error.message); render(); }
		return;
	}
  // Item 2l6 (c): the save that belongs to one setting, beside it.
  if (action === "save-setting") return void applySetting(button.dataset.savePath);
  // Item 2l5 (d): the row save commits THAT connection pending changes and nothing
  // else. It is the explicit save 2l6 leaves in place for this surface.
  if (action === "save-connection") return void saveConnection(id);
  if (action === "settings-section") {
    // Item 2no (a): every section is drawn in the pane, the Plan included. This used
    // to be location.assign('/plan...') — a page navigation dressed as a section.
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
    try { await api("/api/profiles", { action: "create", name }); actionDrafts.delete("new-profile-name"); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
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
    try { await api("/api/profiles", { action: "rename", name: id, new_name: name }); actionDrafts.delete(`rename-profile-${id}`); reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") }); }
    catch (error) { errors.set("profiles", error.message); }
    return render();
  }
	if (action === "skill-toggle") {
		try { await api("/api/skills", {action:"enable", name:id, enabled:button.dataset.value === "true"}); reduce({type:"snapshot", data:await api("/api/state",undefined,"GET")}); }
		catch(error) { errors.set("profiles",error.message); }
		return render();
	}
	if (action === "skill-import" || action === "skill-rescan") {
		try { await api("/api/skills", action === "skill-import" ? {action:"import", path:sheet.querySelector("#skill-import-path")?.value || ""} : {action:"rescan"}); if(action==="skill-import")actionDrafts.delete("skill-import-path"); reduce({type:"snapshot",data:await api("/api/state",undefined,"GET")}); }
		catch(error) { errors.set("profiles",error.message); }
		return render();
	}
	if (action === "hermes-preview") {
		try { const result = await api("/api/skills", {action:"hermes-preview", path:sheet.querySelector("#hermes-import-path")?.value || "~/.hermes"}); hermesPreview=result.hermes; hermesReport=null; }
		catch(error) { errors.set("profiles",error.message); }
		return render();
	}
	if (action === "hermes-toggle") {
		const row=hermesPreview?.rows?.find((item)=>item.id===id); if(row?.selectable)row.included=!row.included;
		return render();
	}
	if (action === "hermes-confirm") {
		try { const result=await api("/api/skills", {action:"hermes-import", path:sheet.querySelector("#hermes-import-path")?.value || "~/.hermes", include:(hermesPreview?.rows||[]).filter((row)=>row.included).map((row)=>row.id)}); hermesReport=result.hermes; hermesPreview=null; actionDrafts.delete("hermes-import-path"); reduce({type:"snapshot",data:await api("/api/state",undefined,"GET")}); }
		catch(error) { errors.set("profiles",error.message); }
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
	if (action.startsWith("trusted-folder-")) {
		const folders = [...(store.config.shell?.trusted_folders || [])];
		if (action === "trusted-folder-delete") folders.splice(Number(id), 1);
		else {
			const input = sheet.querySelector(action === "trusted-folder-add" ? "#trusted-folder-new" : `#trusted-folder-${CSS.escape(id)}`);
			const path = input?.value.trim(); if (!path) return;
			const entry = { path, added_at: new Date().toISOString(), source: "settings" };
			if (action === "trusted-folder-add") folders.push(entry); else folders[Number(id)] = entry;
		}
		try { const config = await api("/api/config", {shell:{trusted_folders:folders}}); reduce({type:"config.changed",data:{config}}); }
		catch (error) { errors.set("shell.trusted_folders", error.message); }
		for (const key of [...actionDrafts.keys()]) if (key.startsWith("trusted-folder-")) actionDrafts.delete(key);
		return render();
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
  // Item 2mf (e): SETTINGS BRINGS A HIDDEN SURFACE BACK. This is the promise the
  // hide confirmation makes in the strip, so it belongs to that item rather than
  // to a follow-up. The stored value is the list of hidden names, so a switch
  // here adds or removes one name and saves the whole list; the surface itself
  // was never discarded, and it returns pinned at the far right because that is
  // where the surface list puts it.
  if (action === "surface-visible") {
    const kind = button.dataset.surface;
    const show = button.dataset.value === "true";
    const hidden = new Set(Array.isArray(store.config.chat?.hidden_surfaces) ? store.config.chat.hidden_surfaces : []);
    show ? hidden.delete(kind) : hidden.add(kind);
    drafts.set("chat.hidden_surfaces", [...hidden].sort().join(","));
    draftKinds.set("chat.hidden_surfaces", "list");
    settingsSaveAlarm = false;
    render();
    return void applySetting("chat.hidden_surfaces");
  }
  if (action === "probe") {
    const pendingPrefix = `connections.${id}.`;
    const connection = connectionList().find((x) => x.id === id);
    if (connection) connection._probing = true;
    // (c): visible within the same frame as the click. The line names the address it
    // is connecting to before any request has answered, and the walk's own events
    // replace it as they land.
    const typedAddress = current(`connections.${id}.base_url`, connection?.base_url || "") || "the address";
    probeMessages.set(id, { message: "", alarm: false, walking: { line: `connecting to ${typedAddress}`, processed: 0, total: 0 } });
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
      // Item 2nn (a): THE ADDRESS FIELD IS HIS. A host typed with no port comes back
      // as "port_required" with what each port answered, and NOTHING is written into
      // the field — the note names the ports and he adds the one he wants. The only
      // value Test still proposes into base_url is a PATH on the port he typed
      // himself (item 2l1's path discovery, which 2nb (c)-(h) keeps), never another
      // port: that is what put :11434 in his field after he typed the bare address.
      if (discovered.changes?.base_url) {
        drafts.set(`${pendingPrefix}base_url`, discovered.changes.base_url);
        draftKinds.set(`${pendingPrefix}base_url`, "text");
        settingsSaveMessage = "Unsaved discovery change";
      }
      // The walk is finished the moment Test answers, however it answered.
      probeMessages.set(id, { ...(probeMessages.get(id) || {}), walking: null });
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
      probeMessages.set(id, {
        ...(probeMessages.get(id) || {}),
        measureRunning: !currentState.running,
        message: currentState.running ? "Evaluation Harness stopping" : "",
        alarm: false,
        harnessWalking: currentState.running ? null : { line: "brief 0 of 10 — thinking off", processed: 0, total: 10 },
      });
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
	// Item 2np (a): THE TOGGLE IS THE SETUP. It used to be a Save draft, and saving it
	// was refused by the server — "turn on the service identity with its Security setup
	// flow so the account and credential are tested first" — which pointed the operator
	// at a panel rather than doing the thing he had just asked for. Switching it on runs
	// the same provision Repair runs, at once; switching it off is immediate the same
	// way. The server guard stays where it is; the UI can no longer reach it.
	if (action === "service-identity-toggle") {
		const on = !!store.config.shell?.service_account?.enabled;
		if (!on) return setupServiceAccount();
		try {
			const result = await api("/api/config", { shell: { service_account: { enabled: false } } });
			reduce({ type: "config.changed", data: { config: result } });
			serviceAccountMessage = "service identity off — tools run as you";
			serviceAccountAlarm = false;
		} catch (error) {
			serviceAccountMessage = error.message;
			serviceAccountAlarm = true;
		}
		await refreshServiceAccountStatus(true);
		return render();
	}
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
	// Item 2kq (b): pairing a phone is three presses — Pair, compare, They match — and
	// each one is an action rather than a setting, because none of them is a value.
	if (action === "broker-pair" || action === "broker-confirm" || action === "broker-revoke" || action === "broker-cancel") {
		const verb = { "broker-pair": "pair", "broker-confirm": "confirm", "broker-revoke": "revoke", "broker-cancel": "cancel" }[action];
		if (verb === "revoke" && !armed.has("broker:revoke")) { armed.add("broker:revoke"); return render(); }
		armed.delete("broker:revoke");
		brokerMessage = verb === "pair" ? "asking the broker for a pairing code…" : "";
		brokerAlarm = false;
		render();
		try {
			await api("/api/broker", { action: verb });
			brokerMessage = verb === "pair" ? "scan the square with the phone, then compare the fingerprint"
				: verb === "confirm" ? "paired"
				: verb === "cancel" ? "pairing cancelled"
				: "the pairing is revoked";
		} catch (error) {
			brokerMessage = error.message;
			brokerAlarm = true;
		}
		await refreshBrokerStatus();
		if (verb === "pair" || verb === "confirm") watchBrokerPairing();
		return render();
	}
	// Item 2nv (c): adding a credential is a masked field that never echoes, and removing
	// one is immediate. The value goes straight to the endpoint and is never held in the
	// page's state, so a re-render cannot put it back on the screen.
	if (action === "credential-add") {
		const field = (id) => sheet.querySelector(id)?.value || "";
		const secret = field("#credential-secret");
		credentialMessage = "";
		credentialAlarm = false;
		try {
			credentialList = (await api("/api/credentials", {
				action: "add", name: field("#credential-name").trim(), origin: field("#credential-origin").trim(),
				header: field("#credential-header").trim(), secret,
			})).credentials || [];
			for (const id of ["#credential-name", "#credential-origin", "#credential-header", "#credential-secret"]) {
				const input = sheet.querySelector(id);
				if (input) input.value = "";
				actionDrafts.delete(id.slice(1));
			}
			credentialMessage = "stored";
		} catch (error) {
			credentialMessage = error.message;
			credentialAlarm = true;
		}
		return render();
	}
	if (action === "credential-entra-add") {
		const field = (selector) => sheet.querySelector(selector)?.value?.trim() || "";
		credentialMessage = "";
		credentialAlarm = false;
		try {
			credentialList = (await api("/api/credentials", {
				action: "add_entra", name: field("#credential-entra-name"), origin: field("#credential-entra-origin"),
				tenant: field("#credential-tenant"), client_id: field("#credential-client-id"), scopes: field("#credential-scopes"),
			})).credentials || [];
			for (const selector of ["#credential-entra-name", "#credential-entra-origin", "#credential-tenant", "#credential-client-id", "#credential-scopes"]) {
				const input = sheet.querySelector(selector);
				if (input) input.value = "";
				actionDrafts.delete(selector.slice(1));
			}
			credentialMessage = "Entra credential added — sign in when ready";
		} catch (error) {
			credentialMessage = error.message;
			credentialAlarm = true;
		}
		return render();
	}
	if (action === "credential-sign-in" || action === "credential-device-code" || action === "credential-sign-out") {
		const verb = { "credential-sign-in": "sign_in", "credential-device-code": "device_code", "credential-sign-out": "sign_out" }[action];
		credentialMessage = verb === "sign_in" ? "Finish sign-in in the browser…" : verb === "device_code" ? "Requesting a device code…" : "Signing out…";
		credentialAlarm = false;
		render();
		try {
			const result = await api("/api/credentials", { action: verb, name: id });
			if (verb === "device_code") {
				credentialDevice = { name: id, ...result };
				credentialMessage = result.message || "Use the code shown above";
				void watchCredentialSignIn(id);
			} else {
				credentialList = result.credentials || [];
				credentialDevice = {};
				credentialMessage = verb === "sign_out" ? "signed out" : "signed in";
			}
		} catch (error) {
			credentialMessage = error.message;
			credentialAlarm = true;
		}
		return render();
	}
	if (action === "credential-remove") {
		if (!armed.has("credential:" + id)) { armed.add("credential:" + id); return render(); }
		armed.delete("credential:" + id);
		try {
			credentialList = (await api("/api/credentials", { action: "remove", name: id })).credentials || [];
			credentialMessage = "removed";
			credentialAlarm = false;
		} catch (error) {
			credentialMessage = error.message;
			credentialAlarm = true;
		}
		return render();
	}
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
	if (action === "sign-in-start") {
		signInStart = { ...signInStart, busy: true, error: "" }; render();
		try { signInStart = { loaded: true, busy: false, error: "", ...await api("/api/sign-in-start", { enabled: !signInStart.enabled }) }; }
		catch (error) { signInStart = { ...signInStart, busy: false, error: error.message }; }
		return render();
	}
	// Item 2mv: the export is a plain read, saved through the browser. No new probe,
	// no elevation, and the file is already redacted by the time it arrives here.
	if (action === "export-diagnostics") {
		try {
			const body = await api("/api/diagnostics", undefined, "GET");
			const stamp = new Date().toISOString().replace(/[:.]/g, "-").slice(0, 19);
			const blob = new Blob([JSON.stringify(body, null, 2)], { type: "application/json" });
			const link = document.createElement("a");
			link.href = URL.createObjectURL(blob);
			link.download = `agent_b-diagnostics-${stamp}.json`;
			link.click();
			URL.revokeObjectURL(link.href);
		} catch (error) {
			settingsSaveMessage = `Export failed: ${error.message}`;
			settingsSaveAlarm = true;
			render();
		}
		return;
	}
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
    if (state?.running) {
      // The bar follows the run: its line is the brief and the arm the server is
      // on, and the fraction is real because the brief count is fixed.
      const seat = sheet.querySelector(`[data-harness-wait="${CSS.escape(id)}"] .wait`);
      const walking = { line: state.text || "running the briefs", processed: Number(state.processed) || 0, total: Number(state.total) || 0 };
      probeMessages.set(id, { ...(probeMessages.get(id) || {}), harnessWalking: walking });
      if (seat) { seat.querySelector(".wait-line").textContent = walking.line; setWaitProgress(seat, walking); }
      else if (open && activeSection === "connections") render();
      continue;
    }
    if (!state) continue;
    probeMessages.set(id, { ...(probeMessages.get(id) || {}), measureRunning: false, harnessWalking: null, message: state.error || state.text || "Evaluation Harness complete", alarm: !!state.error });
    reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
    if (open && activeSection === "connections") render();
    return;
  }
}

// Item 2ns (b) and 2oe: pairing and phone presence both change independently of a press.
// Keep watching for the whole time Settings is open so an absent phone becomes present,
// or a live phone becomes absent, without a reload.
let brokerWatch = null;
function watchBrokerPairing() {
	if (brokerWatch) return;
	brokerWatch = setInterval(async () => {
		await refreshBrokerStatus();
		if (open && activeSection === "shell") render();
	}, 1500);
}

async function refreshCredentials() {
	try { credentialList = (await api("/api/credentials", undefined, "GET")).credentials || []; }
	catch (error) { credentialList = []; credentialMessage = error.message; credentialAlarm = true; }
}

async function watchCredentialSignIn(name) {
	for (let attempt = 0; attempt < 300; attempt += 1) {
		await new Promise((resolve) => setTimeout(resolve, 1000));
		await refreshCredentials();
		const entry = credentialList.find((item) => item.name === name);
		if (entry?.account) {
			credentialDevice = {};
			credentialMessage = `signed in as ${entry.account}`;
			credentialAlarm = false;
			if (open) render();
			return;
		}
	}
}

async function refreshBrokerStatus() {
	try { brokerStatus = await api("/api/broker/status", undefined, "GET"); }
	catch { brokerStatus = { state: "unknown" }; }
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

async function refreshSignInStart() {
	try { signInStart = { loaded: true, busy: false, error: "", ...await api("/api/sign-in-start", undefined, "GET") }; }
	catch (error) { signInStart = { ...signInStart, loaded: true, busy: false, error: error.message }; }
	if (open && activeSection === "about") render();
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
	serviceAccountSteps = [];
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
		serviceAccountLog = result.log || "";
		serviceAccountSteps = result.steps || [];
		serviceAccountAlarm = !result.ok;
		await refreshHardeningStatus();
	} catch (error) {
		if (error.data?.credential) store.shell_credential = error.data.credential;
		serviceAccountLog = error.data?.log || "";
		serviceAccountSteps = error.data?.steps || [];
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
  // Item 2nb (c): the one explicit way out of the dropdown, for a server that cannot
  // enumerate its models. Choosing it turns the control into a field and saves
  // nothing by itself.
  if (event.target.matches('.setting-input[data-path$=".model"]') && event.target.value === "__type__") {
    const id = event.target.dataset.path.split(".")[1];
    typedModels.add(id);
    drafts.delete(event.target.dataset.path);
    render();
    return;
  }
  if (event.target.matches("#hardening-connection")) {
    hardeningConnectionID = event.target.value;
    hardeningStatus = { loaded: false, supported: true, applied: false };
    hardeningMessage = "";
    render();
    await refreshHardeningStatus();
    return;
  }
  // Item 2nb (d): PICKING A MODEL FILLS THE REST. Until now Test filled the fields and
  // picking a different model from the list left them describing the one Test happened
  // to try. Nothing here is typed by the operator and nothing here saves.
  if (event.target.matches('.setting-input[data-path$=".model"]') && event.target.value && event.target.value !== "__type__") {
    fillFromPickedModel(event.target.dataset.path.split(".")[1], event.target.value);
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
    // Item 2nq (e): a refusal about a CONNECTION is shown on that connection's row as
    // well as under its field, and the row is expanded, so a refusal caused by one
    // connection is never an unexplained failure about another. The drafts are
    // untouched here by design — a refused save keeps everything he typed.
    errors.set(error.field || "config", error.message);
    const connection = /^connections\.([A-Za-z0-9-]+)\./.exec(error.field || "")?.[1];
    if (connection) {
      errors.set(`connections.${connection}`, error.message);
      expanded.add(connection);
    }
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
    // Item 2nb (h) and (c): a new connection carries NO address and NO model. The old
    // defaults were a guess and a placeholder: 127.0.0.1:8000 is not where his server
    // is, and the literal "model" reached save and was refused elsewhere with wording
    // he could not act on. Empty fields with placeholders say what to do instead.
    const result = await api("/api/config", { connections: [{ id, label: id, base_url: "", model: "" }] });
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
    errors.delete(`connections.${id}`);
    armed.delete(key);
  } catch (error) {
    // Item 2mb (c): USE THE FIELD THE SERVER SENT. It is what lets the refusal
    // land on the row that was clicked; a handler that discards it is how the
    // message went missing. The per-row key stays as the fallback, so a refusal
    // from an older build still lands where the operator is looking.
    // Item 2nc (a): the row is EXPANDED when a refusal lands, so the reason and the
    // fields it is about are on screen together. (d): after Remove there are exactly
    // two outcomes, and both are visible in this render — the row gone, or the refusal
    // on it. An unchanged screen is not one of them.
    errors.set(error.field || `connections.${id}`, error.message);
    expanded.add(id);
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
    actionDrafts.delete("new-session-label");
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
