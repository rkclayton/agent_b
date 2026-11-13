import { acquireBrowserSessionToken } from "./browser-session.js";
import { largestFitting } from "./local-recommendations.js";

const root = document.getElementById("setup");
const connectionStatus = document.getElementById("connection");
const fullTools = ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service", "delegate"];
const suggestedContext = 8192;
let snapshot, detection, catalog, installState;
let screen = 1, connectionID = "";
let addingFromSettings = false, more = false, installing = false, checking = false, contextNeedsInput = false;
let draft = { label: "My model", base_url: "http://127.0.0.1:8080", api_key: "", model: "", context: 0 };
let checkRows = emptyRows(), models = [], modelDefaults = {}, failure = null, telemetryChoice = true;
const mutationToken = await acquireBrowserSessionToken();

root.addEventListener("click", click);
root.addEventListener("change", change);
root.addEventListener("input", input);
document.querySelector(".shell-window-controls")?.addEventListener("click", (event) => {
  const action = event.target.closest(".shell-window-control")?.dataset.action;
  if (action) void request("/api/host-window", { action }).catch(showHeaderError);
});
void load();

function emptyRows() {
  return ["Server", "Model", "Context size", "Test message"].map((label) => ({ label, state: "wait", value: "" }));
}

async function load() {
  try {
    snapshot = await request("/api/state", undefined, "GET");
    addingFromSettings = new URLSearchParams(location.search).get("from") === "settings";
    telemetryChoice = snapshot.config?.telemetry?.enabled !== false;
    if (!addingFromSettings) {
      const current = snapshot.connections?.find((item) => item.id === snapshot.config?.agents?.[0]?.b) || snapshot.connections?.[0];
      if (current) {
        connectionID = current.id;
        draft = { label: current.label || "My model", base_url: current.base_url || draft.base_url, api_key: "", model: current.model || "", context: Number(current.context?.n_ctx || 0) };
      }
    }
    render();
    void readInstallChoice();
  } catch (error) { showHeaderError(error); }
}

function render() {
  if (!snapshot) return;
  root.innerHTML = `<section class="setup-wizard raised"><div class="setup-head"><span class="setup-step">${screen} OF 3</span><button type="button" class="setup-x" data-action="close" title="Close setup" aria-label="Close setup">×</button></div>${screen === 1 ? chooseScreen() : screen === 2 ? checkingScreen() : readyScreen()}</section>`;
}

function chooseScreen() {
  const chosen = recommendedModel();
  const size = chosen?.artifact?.bytes ? bytes(chosen.artifact.bytes) : chosen?.min_gib ? `${chosen.min_gib} GiB` : "";
  const installFact = chosen ? `${html(chosen.label)}${size ? ` · ${html(size)}` : ""}` : "";
  const progress = installing
    ? `<div class="setup-progress"><progress max="${Number(installState?.total_bytes || 1)}" value="${Number(installState?.downloaded_bytes || 0)}"></progress><span class="setup-fact">${html(progressText())}</span><button type="button" data-action="cancel-install">Cancel</button></div>`
    : `<button type="button" class="setup-primary" data-action="install" ${chosen ? "" : "disabled"}>Install</button>`;
  return `<h1>Set up a model</h1><div class="setup-option"><strong>Install on this computer</strong><span class="setup-fact">${installFact}</span>${progress}</div><div class="setup-option"><strong>Connect to a server</strong><div class="setup-line"><input data-field="address" aria-label="Address" value="${attr(draft.base_url)}"><button type="button" data-action="connect">Connect</button></div><button type="button" class="setup-more" data-action="more">More options ${more ? "⌄" : "›"}</button>${more ? `<div class="setup-grid"><label>Name</label><input data-field="name" aria-label="Name" value="${attr(draft.label)}"><label>API key</label><input data-field="api-key" aria-label="API key" type="password" autocomplete="off" value="${attr(draft.api_key)}"><label>Model</label><input data-field="model" aria-label="Model" value="${attr(draft.model)}"><label>Context size</label><input data-field="context" aria-label="Context size" type="number" value="${draft.context > 0 ? draft.context : ""}"></div>` : ""}</div>`;
}

function checkingScreen() {
  return `<h1>Checking your model</h1><div class="setup-checks">${checkRows.map((row, index) => checkRow(row, index)).join("")}</div><div class="setup-foot"><button type="button" class="setup-quiet setup-back" data-action="back">Back</button>${failure ? `<button type="button" data-action="retry">Try again</button>` : ""}${checksReady() ? `<button type="button" class="setup-primary" data-action="next">Next</button>` : ""}</div>`;
}

function checkRow(row, index) {
  const mark = row.state === "ok" ? "✓" : row.state === "bad" ? "×" : "·";
  let value = html(row.value);
  if (index === 1 && row.state === "pick") value = `<select data-field="model-pick" aria-label="Model"><option value=""></option>${models.map((model) => `<option value="${attr(model)}">${html(model)}</option>`).join("")}</select>`;
  if (index === 2 && contextNeedsInput) value = `<span class="setup-line"><input data-field="context-pick" aria-label="Context size" type="number" value="${draft.context || suggestedContext}"><span class="setup-fact">suggested</span></span>`;
  if (row.state === "bad") value = `<span class="setup-failure-line">${html(row.value)}</span>${failure?.detail ? `<details><summary>details</summary><span>${html(failure.detail)}</span></details>` : ""}`;
  return `<div class="setup-check ${row.state}"><span class="setup-mark">${mark}</span><span>${html(row.label)}</span><span class="setup-check-value">${value}</span></div>`;
}

function readyScreen() {
  const diagnostics = addingFromSettings ? "" : `<button type="button" class="setup-switch ${telemetryChoice ? "on" : ""}" role="switch" aria-checked="${telemetryChoice}" data-action="telemetry"></button><span>Send anonymous diagnostics</span>`;
  return `<h1>Agent_b is ready</h1><div class="setup-check ok"><span class="setup-mark">✓</span><span>Model</span><span class="setup-check-value">${html(draft.model)} · ${Number(draft.context).toLocaleString()} context</span></div>${diagnostics ? `<div class="setup-line">${diagnostics}</div>` : ""}<div class="setup-foot"><button type="button" class="setup-quiet setup-back" data-action="back">Back</button><button type="button" class="setup-primary" data-action="finish">Finish</button></div>`;
}

async function click(event) {
  const action = event.target.closest("[data-action]")?.dataset.action;
  if (!action) return;
  if (action === "close") return closeSetup();
  if (action === "more") { captureDraft(); more = !more; return render(); }
  if (action === "connect") { captureDraft(); return startChecks(); }
  if (action === "install") return installModel();
  if (action === "cancel-install") return cancelInstall();
  if (action === "back") { screen = Math.max(1, screen - 1); failure = null; return render(); }
  if (action === "retry") return runChecks(failure?.index || 0);
  if (action === "next") { await saveCheckedConnection(); screen = 3; return render(); }
  if (action === "telemetry") { telemetryChoice = !telemetryChoice; return render(); }
  if (action === "finish") return finish();
}

function change(event) {
  if (event.target.matches('[data-field="model-pick"]')) {
    draft.model = event.target.value;
    if (draft.model) void continueAfterModel();
  }
}

function input(event) {
  if (!event.target.matches('[data-field="context-pick"]')) return;
  draft.context = Number(event.target.value || 0);
  checkRows[2] = { label: "Context size", state: draft.context > 0 ? "ok" : "wait", value: draft.context > 0 ? draft.context.toLocaleString() : "" };
  render();
}

function captureDraft() {
  draft.base_url = field("address") || draft.base_url;
  draft.label = field("name") || draft.label;
  draft.api_key = field("api-key") || draft.api_key;
  draft.model = field("model") || draft.model;
  const context = Number(field("context") || draft.context || 0);
  if (context > 0) draft.context = context;
}

async function readInstallChoice() {
  try {
    const [local, install] = await Promise.all([request("/api/local-detection", undefined, "GET"), request("/api/model-install", undefined, "GET")]);
    detection = local; catalog = install.catalog; installState = install.state;
  } catch { detection = {}; catalog = { models: [] }; }
  render();
}

function recommendedModel() {
  if (!catalog?.models?.length) return null;
  const available = Math.floor(Number(localRecommendationBytes(detection)) / 2 ** 30);
  return largestFitting(catalog.models.filter((item) => !available || available >= item.min_gib), available);
}

async function installModel() {
  const model = recommendedModel();
  if (!model) return;
  installing = true;
  installState = { running: true, total_bytes: model.artifact?.bytes || 1, downloaded_bytes: 0 };
  render();
  try {
    await request("/api/model-install", { model_id: model.id, backend: localBackend(detection).backend });
    while (installing) {
      await delay(300);
      installState = (await request("/api/model-install", undefined, "GET")).state;
      if (!installState.running) break;
      render();
    }
    if (!installing || installState.phase === "cancelled") return;
    if (installState.error) throw new Error(installState.error);
    snapshot = await request("/api/state", undefined, "GET");
    connectionID = installState.connection_id;
    const saved = snapshot.connections?.find((item) => item.id === connectionID);
    draft = { label: saved?.label || "My model", base_url: saved?.base_url || installState.base_url, api_key: "", model: saved?.model || installState.model, context: Number(saved?.context?.n_ctx || installState.context?.n_ctx || 0) };
    installing = false;
    await startChecks();
  } catch (error) { installing = false; showHeaderError(error); render(); }
}

async function cancelInstall() {
  try { installState = await request("/api/model-install", undefined, "DELETE"); } catch (error) { showHeaderError(error); }
  installing = false;
  render();
}

async function closeSetup() {
  if (installing) await cancelInstall();
  location.href = "/chat?setup=skip&from=setup";
}

async function startChecks() {
  screen = 2; checking = true; failure = null; checkRows = emptyRows(); models = []; modelDefaults = {}; contextNeedsInput = false; render();
  await runChecks(0);
}

async function runChecks(from) {
  checking = true; failure = null;
  for (let index = from; index < checkRows.length; index++) checkRows[index] = { ...checkRows[index], state: "wait", value: "" };
  render();
  try {
    if (from <= 1) {
      const result = await request(`/api/connections/${encodeURIComponent(connectionID || "setup-model")}/models`, { base_url: draft.base_url, api_key: draft.api_key });
      checkRows[0] = { label: "Server", state: "ok", value: "answered" };
      models = result.models || [];
      modelDefaults = result.model_defaults || {};
      if (!models.length) return failRow(1, "No model is loaded on that server.", result.message || "The server listed no models.");
      if (!draft.model && models.length > 1) { checkRows[1] = { label: "Model", state: "pick", value: "" }; checking = false; return render(); }
      if (!draft.model) draft.model = models[0];
      if (!models.includes(draft.model)) return failRow(1, "No model is loaded on that server.", `The server did not list ${draft.model}.`);
      checkRows[1] = { label: "Model", state: "ok", value: draft.model };
      const published = Number(modelDefaults?.[draft.model]?.context?.n_ctx || 0);
      if (draft.context <= 0) draft.context = published || suggestedContext;
      contextNeedsInput = published <= 0;
      checkRows[2] = { label: "Context size", state: draft.context > 0 ? "ok" : "wait", value: contextNeedsInput ? "" : draft.context.toLocaleString() };
      render();
    }
    await runTestMessage();
  } catch (error) {
    const detail = error.message || String(error);
    if (error.status === 401 || error.status === 403 || /refused (?:the )?(?:api )?key|(?:401|403)/i.test(detail)) return failRow(0, "The server refused the key.", detail);
    return failRow(0, "Nothing answered at that address.", detail);
  } finally { checking = false; render(); }
}

async function continueAfterModel() {
  checkRows[1] = { label: "Model", state: "ok", value: draft.model };
  const published = Number(modelDefaults?.[draft.model]?.context?.n_ctx || 0);
  draft.context = published || suggestedContext; contextNeedsInput = published <= 0;
  checkRows[2] = { label: "Context size", state: "ok", value: contextNeedsInput ? "" : draft.context.toLocaleString() };
  render();
  await runTestMessage();
}

async function runTestMessage() {
  try {
    const answer = await request(`/api/connections/${encodeURIComponent(connectionID || "setup-model")}/probe`, { base_url: draft.base_url, model: draft.model, api_key: draft.api_key, request_timeout_s: 30, setup: true });
    if (answer.status !== "passed") throw new Error(answer.message || "The model returned no text.");
    checkRows[3] = { label: "Test message", state: "ok", value: "answered" };
    await saveCheckedConnection();
    render();
  } catch (error) {
    const detail = error.message || String(error);
    if (error.status === 401 || error.status === 403 || /refused (?:the )?(?:api )?key|(?:401|403)/i.test(detail)) return failRow(0, "The server refused the key.", detail);
    if (/no models|model.*not.*served|choose a listed model/i.test(detail)) return failRow(1, "No model is loaded on that server.", detail);
    return failRow(3, "The model did not answer.", detail);
  }
}

async function saveCheckedConnection() {
  if (!(draft.context > 0)) return;
  if (!connectionID || !snapshot.config?.connections?.some((item) => item.id === connectionID)) connectionID = uniqueID("setup-model");
  const current = snapshot.config?.connections?.find((item) => item.id === connectionID) || {};
  const next = { ...current, id: connectionID, label: draft.label || "My model", base_url: draft.base_url, model: draft.model, context: { ...(current.context || {}), n_ctx: draft.context } };
  if (String(next.api_key || "").includes("••••")) delete next.api_key;
  if (draft.api_key) next.api_key = draft.api_key;
  snapshot.config = await request("/api/config", { connections: [next] });
  snapshot = await request("/api/state", undefined, "GET");
}

function failRow(index, line, detail) {
  checkRows[index] = { label: checkRows[index].label, state: "bad", value: line };
  for (let next = index + 1; next < checkRows.length; next++) checkRows[next] = { ...checkRows[next], state: "wait", value: "" };
  failure = { index, detail }; checking = false; render();
}

function checksReady() { return !checking && !failure && draft.context > 0 && checkRows.every((row) => row.state === "ok"); }

async function finish() {
  try {
    const patch = { agents: savedConnectionAgents() };
    if (!addingFromSettings) patch.telemetry = { ...(snapshot.config?.telemetry || {}), enabled: telemetryChoice };
    snapshot.config = await request("/api/config", patch);
    snapshot = await request("/api/state", undefined, "GET");
    const agent = snapshot.config?.agents?.[0];
    if (!Object.keys(snapshot.sessions || {}).length && agent) {
      const agentID = agent.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      const created = await request("/api/sessions", { label: "main", agent_id: agentID });
      location.href = `/chat?session=${encodeURIComponent(created.id || created.session?.id || "main")}`;
      return;
    }
    location.href = "/chat";
  } catch (error) { showHeaderError(error); }
}

function savedConnectionAgents() {
  const current = snapshot.config?.agents?.[0] || { name: "Agent_b", toolset: fullTools };
  const agent = { ...current, name: current.name || "Agent_b", toolset: current.toolset || fullTools };
  if (!agent.b) agent.b = connectionID;
  else if (agent.b !== connectionID && !agent.c) agent.c = connectionID;
  return [agent];
}

function progressText() { const done = Number(installState?.downloaded_bytes || 0), total = Number(installState?.total_bytes || 0); return total > 0 ? `${bytes(done)} of ${bytes(total)}` : ""; }
function field(name) { return root.querySelector(`[data-field="${name}"]`)?.value?.trim() || ""; }
function uniqueID(base) { let id = base, n = 2; while (snapshot.config?.connections?.some((item) => item.id === id)) id = `${base}-${n++}`; return id; }
function localBackend(report = {}) { const gpus = report.gpus || []; if (report.accelerators?.cuda) return { backend: "cuda", gpu: gpus.find((item) => item.vendor === "NVIDIA") || gpus[0] }; if (report.accelerators?.vulkan) return { backend: "vulkan", gpu: gpus[0] }; return { backend: "cpu", gpu: null }; }
function localRecommendationBytes(report = {}) { return localBackend(report).gpu?.vram_bytes || report.system_memory_bytes || 0; }
function bytes(value) { const gib = Number(value || 0) / 2 ** 30; return `${gib >= 1 ? gib.toFixed(1) : (Number(value || 0) / 2 ** 20).toFixed(0)} ${gib >= 1 ? "GiB" : "MiB"}`; }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
function showHeaderError(error) { connectionStatus.textContent = error.message || String(error); connectionStatus.className = "alarm"; }
function html(value) { return String(value ?? "").replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]); }
function attr(value) { return html(value); }

async function request(path, body, method = "POST") {
  const options = { method, headers: {} };
  if (method !== "GET" && method !== "HEAD") options.headers["X-AgentB-Mutation-Token"] = mutationToken;
  if (body !== undefined) { options.headers["Content-Type"] = "application/json"; options.body = JSON.stringify(body); }
  const response = await fetch(path, options);
  const value = await response.json().catch(() => ({}));
  if (!response.ok) { const error = new Error(value.error || `HTTP ${response.status}`); error.status = response.status; throw error; }
  return value;
}
