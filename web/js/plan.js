// The Plan: every plan in a flyout, one plan's raw plan.md and its numbers.
//
// IT IS A SETTINGS SECTION NOW, NOT A PAGE. It used to own a document -- it
// called initShell for the whole window, read the address for its state, rewrote that
// address as the selection moved, subscribed to the bus at import and never
// unsubscribed, and started a thirty-second timer nothing ever cleared. All of that is
// page lifetime, and a section has a mount lifetime instead: mountPlan() when Settings
// adopts the panel, unmountPlan() when it gives it back. The nodes themselves are the
// ones index.html carries, moved rather than rebuilt, so every listener wired here at
// import is still wired to the node the operator clicks.
import { api, store, subscribe } from "./bus.js";
import { changedLines, fadeFor, filterPlans, markerSummary } from "./plan-surface.js";

// The page is plan-centric. The left flyout lists every plan with a search and
// a + that registers a plan for a folder and offers "Build plan now?". The
// right side is the selected plan's plan.md, raw and read-only, with lines
// that changed since the page opened (or the last hour) highlighted and
// fading, its numbers beside or below it, and Go in its header. Proposals are
// not drawn here: they sit in the planning chat's own thread.
const byID = (id) => document.getElementById(id);
const roots = {
  search: byID("plan-search"), add: byID("plan-add"), form: byID("plan-add-form"), path: byID("plan-add-path"), addError: byID("plan-add-error"), addPreview: byID("plan-add-preview"),
  previewFolder: byID("plan-preview-folder"), previewRepository: byID("plan-preview-repository"), previewPlan: byID("plan-preview-plan"), previewAgents: byID("plan-preview-agents"),
  list: byID("plan-list"), listEmpty: byID("plan-list-empty"),
  name: byID("plan-name"), enable: byID("plan-enable"), go: byID("plan-go"), lint: byID("plan-lint"), refusal: byID("plan-refusal"), done: byID("plan-done"), raw: byID("plan-raw"), stats: byID("plan-stats"), facts: byID("plan-facts"), chats: byID("plan-chats"), changes: byID("plan-changes"),
};
// The address is read ONCE, at import, before the workspace rewrites it to the chat's
// own path: a deep link of /plan?plan=<id> or ?session=<id> is honoured, and nothing
// here writes to the address afterwards -- a section does not own it.
const params = new URLSearchParams(location.search);
let plans = [];
let selected = params.get("plan") || "";
let fromSession = params.get("session") || "";
let loaded = null;
let loadedFor = "";
let previousText = null;
let changes = new Map();
let goState = { enabled: false, running: false };
let goTimer = 0;
let plansLoaded = false;
let addPreview = null;

let unsubscribe = null;
let rawTimer = 0;

// mountPlan is called when Settings adopts the panel into its Plan section, and it is
// safe to call twice: a second mount while one is live does nothing.
export function mountPlan() {
  if (unsubscribe) return;
  unsubscribe = subscribe((_state, event) => {
    resolveFromSession();
    if (["plan.created", "plan.updated", "plan.removed"].includes(event?.type)) {
      void loadPlans();
      if (event.data?.plan_id === selected) void loadPlan();
    }
    scheduleGo();
  });
  rawTimer = setInterval(renderRaw, 30000);
  void loadPlans();
}

// unmountPlan is called when the panel goes back to its holder. Everything this
// section started stops: no subscriber drawing into nodes nobody can see, no timer,
// no pending Go poll.
export function unmountPlan() {
  if (unsubscribe) unsubscribe();
  unsubscribe = null;
  if (rawTimer) clearInterval(rawTimer);
  rawTimer = 0;
  if (goTimer) clearTimeout(goTimer);
  goTimer = 0;
}

// Opened from a chat (?session=): its bound plan, once both the snapshot and
// the plan list are here; a chat bound to none, or gone, shows the first plan.
function resolveFromSession() {
  if (!fromSession || !store.loaded || !plansLoaded) return;
  const planID = store.sessions[fromSession]?.plan_id;
  fromSession = "";
  const target = planID && plans.some((plan) => plan.id === planID) ? planID : plans[0]?.id;
  // resolving the session is also a render. Handing a target that
  // equals the current selection to select() would return before drawing
  // anything, and with no plans at all the target is undefined — the two
  // paths that left the page showing neither the list nor its empty state.
  if (target && target === selected) return void renderList();
  select(target);
}

async function loadPlans() {
  try { plans = await api("/api/plans", undefined, "GET"); } catch { plans = []; }
  if (!Array.isArray(plans)) plans = [];
  plansLoaded = true;
  if (fromSession) {
    resolveFromSession();
    // the selection needs the snapshot, but the list does not. If the
    // snapshot has not arrived — or never does, because its fetch failed — the
    // page still draws the list or its empty state rather than waiting blank.
    if (fromSession) { renderList(); if (!selected) renderEmpty(); return; }
    return;
  }
  if (!selected || !plans.some((plan) => plan.id === selected)) selected = plans[0]?.id || "";
  renderList();
  if (selected && loadedFor !== selected) await loadPlan();
  if (!selected) renderEmpty();
}

function select(id) {
  // with nothing to select the page still renders — the list or its
  // empty state on the left, the empty pane on the right. `select(undefined)`
  // used to return before any render, which is how a Plan page opened from a
  // chat bound to no plan showed an entirely blank surface.
  if (!id) {
    selected = "";
    renderList();
    renderEmpty();
    return;
  }
  if (id === selected) return;
  selected = id;
  // no address change beyond what other sections do. Choosing a plan
  // used to rewrite the document's address to /plan?plan=<id>; the selection is this
  // section's state, and Settings' own `#settings/plan` is the address.
  renderList();
  void loadPlan();
}

async function loadPlan() {
  const id = selected;
  if (!id) return;
  let data;
  try { data = await api(`/api/plan?plan_id=${encodeURIComponent(id)}`, undefined, "GET"); }
  catch (error) { data = { plan: "", error: error.message, stats: {} }; }
  if (id !== selected) return;
  if (loadedFor !== id) { previousText = null; changes = new Map(); }
  if (previousText !== null) for (const line of changedLines(previousText, data.plan || "")) changes.set(line, Date.now());
  previousText = data.plan || "";
  loaded = data;
  loadedFor = id;
  renderList();
  renderPlan();
  void renderGo();
}

function renderList() {
  const shown = filterPlans(plans, roots.search.value);
  roots.listEmpty.hidden = plans.length > 0;
  roots.list.replaceChildren(...shown.map((plan) => {
    const entry = document.createElement("tr");
    entry.className = `plan-entry${plan.id === selected ? " selected" : ""}`;
    const name = document.createElement("button");
    name.type = "button";
    name.className = "plan-entry-name";
    const nameText = document.createElement("span");
    nameText.textContent = plan.missing ? `repo missing · ${plan.legacy_path || plan.repo || plan.id}` : plan.repo || plan.name || plan.id;
    name.append(nameText);
    name.title = plan.repo || plan.id;
    name.onclick = () => select(plan.id);
    const path = document.createElement("td"); path.append(name);
    const planning = document.createElement("td"); planning.textContent = plan.planning ? "on" : "—";
    const open = document.createElement("td"); open.textContent = String(plan.id === selected ? loaded?.stats?.chats?.length || 0 : plan.open_chats || 0);
    const last = document.createElement("td"); last.textContent = plan.id === selected && loaded?.stats?.last_go ? clock(loaded.stats.last_go) : plan.last_go ? clock(plan.last_go) : "—";
    const action = document.createElement("td");
    if (!plan.planning && !plan.missing) { const add = document.createElement("button"); add.type = "button"; const text = document.createElement("span"); text.textContent = "Add to planning"; add.append(text); add.onclick = () => enablePlanning(plan.id); action.append(add); }
    entry.append(path, planning, open, last, action);
    return entry;
  }));
}

function renderEmpty() {
  roots.name.textContent = "Repository";
  roots.raw.textContent = "";
  roots.stats.replaceChildren();
  roots.facts.replaceChildren();
  roots.chats.replaceChildren();
  roots.changes.replaceChildren();
  roots.go.disabled = true;
  roots.go.hidden = true;
  roots.enable.hidden = true;
  roots.raw.closest("details").hidden = true;
}

function renderPlan() {
  roots.name.textContent = loaded?.missing ? `repo missing · ${loaded.legacy_path || loaded.repo || selected}` : loaded?.repo || loaded?.name || selected || "Repository";
  roots.enable.hidden = !loaded || loaded.planning || loaded.missing;
  roots.go.hidden = !loaded?.planning;
  roots.refusal.hidden = !loaded?.refusal;
  roots.refusal.textContent = loaded?.refusal || "";
  renderRaw();
  renderStats();
}

function renderRaw() {
  if (!loaded) return;
  roots.raw.closest("details").hidden = !loaded.planning;
  if (loaded.error) { roots.raw.textContent = loaded.error; return; }
  const now = Date.now();
  roots.raw.replaceChildren(...(loaded.plan || "").replaceAll("\r", "").split("\n").map((text) => {
    const row = document.createElement("span");
    row.className = "plan-line";
    row.textContent = `${text}\n`;
    const fade = fadeFor(changes.get(text), now);
    if (fade > 0) { row.classList.add("changed"); row.style.setProperty("--fade", fade.toFixed(2)); }
    return row;
  }));
}

function renderStats() {
  const stats = loaded?.stats || {};
  const facts = loaded?.facts || {};
  const repo = facts.repository || {};
  const rows = [
    ["Folder", facts.folder || loaded?.repo || "none", facts.folder],
    ["Repository", [repo.root, repo.branch, repo.subject, repo.day].filter(Boolean).join(" · ") || "not a repository", repo.root],
    ["Planning", loaded?.planning ? `${facts.plan_path} · ${markerSummary(stats.markers)}` : "—", facts.plan_path],
    ["AGENTS.md", facts.agents_path ? `${facts.agents_path} · ${facts.agents_size} bytes` : "not found", facts.agents_path],
    ["Notes", `${facts.notes_count || 0}`, facts.notes_path],
  ];
  roots.facts.replaceChildren(...rows.flatMap(([term, value, path]) => {
    const name = document.createElement("dt"); name.textContent = term;
    const detail = document.createElement("dd");
    if (path) { const link = document.createElement("a"); link.href = fileURL(path); link.textContent = value; detail.append(link); } else detail.textContent = value;
    return [name, detail];
  }));
  roots.chats.replaceChildren(...(stats.chats || []).map((chat) => tableRow(linkToChat(chat.id, chat.label || chat.id), clock(chat.last_activity))));
  roots.changes.replaceChildren(...(stats.changes || []).map((change) => tableRow(clock(change.when), linkToChat(change.chat_id, change.chat || change.chat_id, change.run_id), change.files ?? "—")));
}

// Go reads its state for the selected plan; it is refused with the reason as
// its title when the plan cannot run or the worker's model is busy.
function scheduleGo() {
  if (goTimer) return;
  goTimer = setTimeout(() => { goTimer = 0; void renderGo(); }, 400);
}

async function renderGo() {
  if (!selected || !loaded?.planning) { roots.go.disabled = true; renderLint([]); roots.done.hidden = true; return; }
  try { goState = await api(`/api/plan/go?plan_id=${encodeURIComponent(selected)}`, undefined, "GET"); }
  catch { goState = { enabled: false, running: false }; }
  roots.go.textContent = goState.running ? "Stop" : "Go";
  roots.go.classList.toggle("running", !!goState.running);
  roots.go.disabled = !goState.running && !goState.enabled;
  roots.go.title = goState.running ? "Stop the worker" : goState.enabled ? "Run the accepted items in plan order" : goState.refusal || "No item is waiting";
  renderLint(goState.diagnostics);
  await renderDone();
}

// the plan lint's findings, one line under the header. An error is
// why Go refuses; warnings (an item with no verifier) change nothing.
export function lintLine(diagnostics = []) {
  if (!diagnostics.length) return { text: "", error: false };
  const errors = diagnostics.filter((entry) => entry.severity === "error");
  const shown = (errors.length ? errors : diagnostics).map((entry) => entry.message);
  return { text: shown.join(" · "), error: errors.length > 0 };
}

function renderLint(diagnostics) {
  const value = lintLine(diagnostics || []);
  roots.lint.hidden = !value.text;
  roots.lint.textContent = value.text;
  roots.lint.classList.toggle("warn", !value.error);
}

roots.go.addEventListener("click", async () => {
  if (!selected) return;
  roots.go.disabled = true;
  try { await api("/api/plan/go", { plan_id: selected, stop: !!goState.running }); }
  catch (error) { roots.done.hidden = false; roots.done.textContent = error.message; }
  finally { void renderGo(); }
});

// The done card: what finished, what is stuck and why. Nothing else.
async function renderDone() {
  let report;
  try { report = await api(`/api/plan/worker?plan_id=${encodeURIComponent(selected)}`, undefined, "GET"); }
  catch { return; }
  if (!report?.done || goState.running) { roots.done.hidden = true; return; }
  const summary = report.summary || {};
  const head = document.createElement("strong");
  head.textContent = summary.stopped ? "Worker stopped" : summary.stuck ? "Ready to test, with gaps" : "Ready to test";
  const first = document.createElement("div");
  first.append(head, ` · ${summary.done || 0} done · ${summary.stuck || 0} stuck · ${summary.waiting || 0} waiting`);
  roots.done.replaceChildren(first);
  if (report.error) roots.done.append(line(report.error));
  if ((summary.reasons || []).length) {
    const list = document.createElement("ul");
    list.append(...summary.reasons.map((reason) => { const entry = document.createElement("li"); entry.textContent = reason; return entry; }));
    roots.done.append(list);
  }
  roots.done.hidden = false;
}

// + asks for a folder (a path, then Enter), registers the plan through the
// same route every other registration takes, and — for a plan it created —
// asks "Build plan now?".
roots.search.addEventListener("input", renderList);
roots.add.addEventListener("click", async () => {
  roots.addError.hidden = true;
  try {
    const picked = await api("/api/folder-picker", { initial: loaded?.repo || "" });
    addPreview = await api("/api/plans", { action: "preview", repo: picked.path });
    roots.path.value = addPreview.folder;
    roots.previewFolder.textContent = addPreview.folder;
    roots.previewRepository.textContent = [addPreview.repository?.root, addPreview.repository?.branch].filter(Boolean).join(" · ") || "not found";
    roots.previewPlan.textContent = addPreview.plan || "—";
    roots.previewAgents.textContent = addPreview.agents || "not found";
    roots.form.hidden = false; roots.addPreview.hidden = false;
  } catch (error) { roots.addError.textContent = error.message; roots.addError.hidden = false; roots.form.hidden = false; }
});
roots.path.addEventListener("keydown", (event) => { if (event.key === "Escape") { roots.form.hidden = true; roots.path.value = ""; } });
roots.form.addEventListener("submit", async (event) => {
  event.preventDefault();
  roots.addError.hidden = true;
  try {
    const result = await api("/api/plans", { repo: addPreview?.folder || roots.path.value });
    roots.form.hidden = true;
    roots.path.value = "";
    await loadPlans();
    select(result.plan.id);
  } catch (error) {
    roots.addError.textContent = error.message;
    roots.addError.hidden = false;
  }
});
roots.enable.addEventListener("click", () => enablePlanning(selected));

async function enablePlanning(id) {
  if (!id) return;
  try {
    await api("/api/plans", { action: "planning", id });
	loadedFor = "";
    await loadPlans();
    select(id);
  } catch (error) {
    roots.addError.textContent = error.message;
    roots.addError.hidden = false;
  }
}

function line(text) { const value = document.createElement("div"); value.textContent = text; return value; }
function clock(iso) { const date = new Date(iso); return Number.isNaN(date.getTime()) ? String(iso) : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
function fileURL(path) { return encodeURI(`file:///${String(path).replaceAll("\\", "/")}`); }
function linkToChat(id, label, run = "") { const link = document.createElement("a"); link.href = `/chat?session=${encodeURIComponent(id)}${run ? `&run=${encodeURIComponent(run)}` : ""}`; link.textContent = label; return link; }
function tableRow(...cells) { const row = document.createElement("tr"); for (const value of cells) { const cell = document.createElement("td"); if (value instanceof Node) cell.append(value); else cell.textContent = String(value); row.append(cell); } return row; }
