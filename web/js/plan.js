// The Plan page (item 2fc): every plan in a flyout, one plan's raw plan.md and its numbers.
import { api, store, subscribe } from "./bus.js";
import { initShell } from "./shell.js";
import { changedLines, fadeFor, filterPlans, markerSummary } from "./plan-surface.js";

// The page is plan-centric. The left flyout lists every plan with a search and
// a + that registers a plan for a folder and offers "Build plan now?". The
// right side is the selected plan's plan.md, raw and read-only, with lines
// that changed since the page opened (or the last hour) highlighted and
// fading, its numbers beside or below it, and Go in its header. Proposals are
// not drawn here: they sit in the planning chat's own thread.
initShell({ page: "plan" });
const byID = (id) => document.getElementById(id);
const roots = {
  search: byID("plan-search"), add: byID("plan-add"), form: byID("plan-add-form"), path: byID("plan-add-path"), addError: byID("plan-add-error"),
  build: byID("plan-build"), buildYes: byID("plan-build-yes"), buildNo: byID("plan-build-no"), list: byID("plan-list"), listEmpty: byID("plan-list-empty"),
  wizard: byID("plan-wizard"), wizardLabel: byID("plan-wizard-label"), wizardValue: byID("plan-wizard-value"), wizardNext: byID("plan-wizard-next"), wizardSkip: byID("plan-wizard-skip"),
  name: byID("plan-name"), go: byID("plan-go"), lint: byID("plan-lint"), refusal: byID("plan-refusal"), done: byID("plan-done"), raw: byID("plan-raw"), stats: byID("plan-stats"),
};
const params = new URLSearchParams(location.search);
let plans = [];
let selected = params.get("plan") || "";
let fromSession = params.get("session") || "";
let loaded = null;
let loadedFor = "";
let previousText = null;
let changes = new Map();
let buildFor = "";
let wizardStep = 0;
let planningBrief = { purpose: "", done: "", do_not_touch: "" };
let goState = { enabled: false, running: false };
let goTimer = 0;
let plansLoaded = false;

void loadPlans();
setInterval(renderRaw, 30000);

subscribe((_state, event) => {
  resolveFromSession();
  if (["plan.created", "plan.updated", "plan.removed"].includes(event?.type)) {
    void loadPlans();
    if (event.data?.plan_id === selected) void loadPlan();
  }
  scheduleGo();
});

// Opened from a chat (?session=): its bound plan, once both the snapshot and
// the plan list are here; a chat bound to none, or gone, shows the first plan.
function resolveFromSession() {
  if (!fromSession || !store.loaded || !plansLoaded) return;
  const planID = store.sessions[fromSession]?.plan_id;
  fromSession = "";
  const target = planID && plans.some((plan) => plan.id === planID) ? planID : plans[0]?.id;
  // Item 2gi: resolving the session is also a render. Handing a target that
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
    // Item 2gi: the selection needs the snapshot, but the list does not. If the
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
  // Item 2gi: with nothing to select the page still renders — the list or its
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
  params.set("plan", id);
  params.delete("session");
  history.replaceState(null, "", `/plan?${params}`);
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
    const entry = document.createElement("li");
    entry.className = `plan-entry${plan.id === selected ? " selected" : ""}`;
    const name = document.createElement("button");
    name.type = "button";
    name.className = "plan-entry-name";
    name.textContent = plan.name || plan.id;
    name.title = plan.repo || plan.id;
    name.onclick = () => select(plan.id);
    entry.append(name);
    if (plan.id === selected && loaded?.stats && loadedFor === plan.id) {
      const facts = document.createElement("div");
      facts.className = "plan-entry-facts";
      facts.append(line(loaded.repo || "no repository"), line(markerSummary(loaded.stats.markers)));
      if (loaded.stats.last_go) facts.append(line(`last Go ${clock(loaded.stats.last_go)}`));
      if (loaded.stats.related?.length) facts.append(line(`${loaded.stats.related.length} related`));
      entry.append(facts);
    }
    return entry;
  }));
}

function renderEmpty() {
  roots.name.textContent = "Plan";
  roots.raw.textContent = "";
  roots.stats.replaceChildren();
  roots.go.disabled = true;
}

function renderPlan() {
  roots.name.textContent = loaded?.name || selected || "Plan";
  roots.refusal.hidden = !loaded?.refusal;
  roots.refusal.textContent = loaded?.refusal || "";
  renderRaw();
  renderStats();
}

function renderRaw() {
  if (!loaded) return;
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
  const rows = [
    ["repository", loaded?.repo || "none"],
    ["items", markerSummary(stats.markers)],
    ["plan.md changed", stats.changed_at ? clock(stats.changed_at) : "unknown"],
    ["last Go", stats.last_go ? `${clock(stats.last_go)}${stats.last_result ? ` · ${stats.last_result.done} done · ${stats.last_result.stuck} stuck · ${stats.last_result.waiting} waiting` : ""}` : "not since Agent_b started"],
    ["related plans", (stats.related || []).map((plan) => plan.name || plan.id).join(", ") || "none"],
    ["planning chats", (stats.chats || []).map((chat) => chat.label || chat.id).join(", ") || "none"],
  ];
  roots.stats.replaceChildren(...rows.flatMap(([term, value]) => {
    const name = document.createElement("dt"); name.textContent = term;
    const detail = document.createElement("dd"); detail.textContent = value;
    return [name, detail];
  }));
}

// Go reads its state for the selected plan; it is refused with the reason as
// its title when the plan cannot run or the worker's model is busy.
function scheduleGo() {
  if (goTimer) return;
  goTimer = setTimeout(() => { goTimer = 0; void renderGo(); }, 400);
}

async function renderGo() {
  if (!selected) return;
  try { goState = await api(`/api/plan/go?plan_id=${encodeURIComponent(selected)}`, undefined, "GET"); }
  catch { goState = { enabled: false, running: false }; }
  roots.go.textContent = goState.running ? "Stop" : "Go";
  roots.go.classList.toggle("running", !!goState.running);
  roots.go.disabled = !goState.running && !goState.enabled;
  roots.go.title = goState.running ? "Stop the worker" : goState.enabled ? "Run the accepted items in plan order" : goState.refusal || "No item is waiting";
  renderLint(goState.diagnostics);
  await renderDone();
}

// Item 2bq: the plan lint's findings, one line under the header. An error is
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
roots.add.addEventListener("click", () => {
  roots.form.hidden = !roots.form.hidden;
  roots.addError.hidden = true;
  if (!roots.form.hidden) roots.path.focus();
});
roots.path.addEventListener("keydown", (event) => { if (event.key === "Escape") { roots.form.hidden = true; roots.path.value = ""; } });
roots.form.addEventListener("submit", async (event) => {
  event.preventDefault();
  roots.addError.hidden = true;
  try {
    const result = await api("/api/plans", { repo: roots.path.value });
    roots.form.hidden = true;
    roots.path.value = "";
    await loadPlans();
    select(result.plan.id);
    buildFor = result.created ? result.plan.id : "";
    roots.build.hidden = !buildFor;
  } catch (error) {
    roots.addError.textContent = error.message;
    roots.addError.hidden = false;
  }
});
roots.buildNo.addEventListener("click", () => { roots.build.hidden = true; buildFor = ""; });
roots.buildYes.addEventListener("click", () => {
  if (!buildFor) return;
  roots.build.hidden = true;
  wizardStep = 0;
  planningBrief = { purpose: "", done: "", do_not_touch: "" };
  renderWizard();
});
roots.wizard.addEventListener("submit", (event) => { event.preventDefault(); advanceWizard(false); });
roots.wizardSkip.addEventListener("click", () => advanceWizard(true));

function renderWizard() {
  const fields = [
    ["purpose", "What is this project for?"],
    ["done", "What does done look like? Three lines are enough."],
    ["do_not_touch", "Anything the planner must not touch?"],
  ];
  const [field, label] = fields[wizardStep];
  roots.wizard.hidden = false;
  roots.wizard.dataset.field = field;
  roots.wizardLabel.textContent = label;
  roots.wizardValue.value = planningBrief[field];
  roots.wizardNext.textContent = wizardStep === fields.length - 1 ? "Start planning" : "Next";
  roots.wizardValue.focus();
}

function advanceWizard(skip) {
  planningBrief[roots.wizard.dataset.field] = skip ? "" : roots.wizardValue.value.trim();
  if (++wizardStep < 3) return renderWizard();
  roots.wizard.hidden = true;
  void startPlanning();
}

async function startPlanning() {
  roots.buildYes.disabled = true;
  try {
    const agentID = store.sessions[store.selection.session_id]?.agent_id || "";
    const result = await api("/api/plans/build", { plan_id: buildFor, agent_id: agentID, brief: planningBrief });
    // v0.70.1 overrule: Yes is the consent, so the server sends the fixed
    // opening request to a new planning chat. A chat already under way is only
    // opened, with the request left in its composer.
    if (!result.sent) { try { sessionStorage.setItem(`agentb.draft.${result.session_id}`, result.draft); } catch { /* the chat opens empty */ } }
    location.href = `/chat?session=${encodeURIComponent(result.session_id)}`;
  } catch (error) {
    roots.addError.textContent = error.message;
    roots.addError.hidden = false;
    roots.buildYes.disabled = false;
  }
}

function line(text) { const value = document.createElement("div"); value.textContent = text; return value; }
function clock(iso) { const date = new Date(iso); return Number.isNaN(date.getTime()) ? String(iso) : date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }); }
