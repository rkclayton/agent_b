const root = document.getElementById("setup");
const connection = document.getElementById("connection");
const fullTools = ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service"];
let snapshot;
let step = "where";
let connectionID = "";
let detection;
let catalog;
let installState;
let busy = false;
let measuring = false;
let message = "";
let alarm = false;
let discoveredModels = [];
let discoveryNote = "";
let connectionDraft;

root.addEventListener("click", click);
root.addEventListener("change", change);
document.querySelector(".shell-window-controls")?.addEventListener("click", (event) => {
  const action = event.target.closest(".shell-window-control")?.dataset.action;
  if (!action) return;
  void request("/api/host-window", { action }).catch((error) => {
    connection.textContent = error.message;
    connection.className = "alarm";
  });
});
void load();

async function load() {
  try {
    snapshot = await request("/api/state", undefined, "GET");
    const addingFromSettings = new URLSearchParams(location.search).get("from") === "settings";
    connectionID = addingFromSettings ? "" : snapshot.config?.agents?.[0]?.b || snapshot.connections?.[0]?.id || "";
    render();
  } catch (error) {
    connection.textContent = error.message;
    connection.className = "alarm";
  }
}

function render() {
  if (!snapshot) return;
  const names = { where: "Where is your model?", capability: "Evaluation Harness", done: "Done" };
  document.getElementById("setup-step").textContent = names[step];
  root.innerHTML = step === "where" ? whereScreen() : step === "capability" ? capabilityScreen() : doneScreen();
}

function whereScreen() {
  const connection = connectionDraft || selectedConnection();
  const currentModel = connection?.model || "";
  const modelField = discoveredModels.length
    ? `<select data-field="model">${!discoveredModels.includes(currentModel) && currentModel ? `<option value="${attr(currentModel)}" selected>${html(currentModel)} · not served</option>` : ""}${discoveredModels.map((model) => `<option value="${attr(model)}" ${model === currentModel ? "selected" : ""}>${html(model)}</option>`).join("")}</select>`
    : `<input data-field="model" value="${attr(currentModel)}" placeholder="model name">`;
  return `<section class="setup-section"><h1>Where is your model?</h1>
    <div class="setup-fields">
      <label>Label<input data-field="label" value="${attr(connection?.label || "My model")}"></label>
      <label>Address<input data-field="url" value="${attr(connection?.base_url || "http://127.0.0.1:8080")}" placeholder="host or http://host:port">${discoveryNote ? `<span class="setup-note discovery-note">${html(discoveryNote)}</span>` : ""}</label>
      <label>Saved credential name<input data-field="credential" value="${attr(connection?.credential || "")}" placeholder="optional"></label>
      <label>API key<input data-field="api-key" type="password" autocomplete="off" placeholder="optional"></label>
      <label>Model<span class="setup-inline">${modelField}<button data-action="query-models" type="button">Query models</button></span></label>
      <label>Context size<input data-field="context" type="number" value="${attr(connection?.context?.n_ctx || "")}" placeholder="${attr(connection?.capabilities?.n_ctx || "")}"></label>
      <label>Reasoning enabled<select data-field="reasoning"><option value="true" ${connection?.reasoning?.enabled !== false ? "selected" : ""}>on</option><option value="false" ${connection?.reasoning?.enabled === false ? "selected" : ""}>off</option></select></label>
      <label>State<strong class="setup-value">${html(connection?.not_runnable_reason || (connection?.capabilities?.probed_at ? "ready" : "not tested"))}</strong></label>
    </div>
    <div class="setup-actions"><button data-action="save" ${disabled()}>Save</button><button data-action="test" ${disabled()}>Test</button><button data-action="show-install" class="quiet">Install one here</button><button data-action="later" class="quiet">Later</button></div>
    ${installer()}${feedback()}</section>`;
}

function installer() {
  if (!detection && !catalog) return "";
  if (!detection || !catalog) return `<div class="setup-install"><p>Reading this computer…</p></div>`;
  const models = catalog.models || [];
  const modelOptions = models.map((item) => `<option value="${attr(item.id)}">${html(item.label)} · ${item.min_gib} GiB minimum</option>`).join("");
  const progress = installState ? `<div class="setup-progress"><progress max="${installState.total_bytes || 1}" value="${installState.downloaded_bytes || 0}"></progress><span>${html(installState.text || installState.phase || "")}</span></div>` : "";
  const choice = localBackend(detection);
  const hardware = choice.gpu
    ? `${choice.gpu.vendor} ${choice.gpu.name} · ${memory(choice.gpu.vram_bytes, "VRAM")}`
    : `${memory(detection.system_memory_bytes, "system memory")} · CPU fallback`;
  return `<div class="setup-install"><h2>Install one here</h2><p>${html(hardware)}</p>
    <div class="setup-fields"><label>Model<select data-field="install-model">${modelOptions}</select></label><label>Backend<strong class="setup-value">${html(choice.backend)}</strong></label></div>
    <p class="setup-note">Sources: ${link(catalog.runtime_source, `llama.cpp ${catalog.runtime_version}`)} and the selected model's named Hugging Face source. Agent_b verifies the pinned size and SHA-256 before extracting or starting anything.</p>
    <div class="setup-actions"><button data-action="install" ${disabled() || !models.length ? "disabled" : ""}>Install and test</button></div>${progress}
    <details><summary>Model runs on another computer</summary><pre>${html(remoteGuide())}</pre></details></div>`;
}

function capabilityScreen() {
  const connection = selectedConnection();
  const caps = connection?.capabilities || {};
  const measurement = connection?.measurement;
  return `<section class="setup-section"><h1>Evaluation Harness</h1>
    <div class="setup-readout">
      ${row("Context", caps.n_ctx ? `${Number(caps.n_ctx).toLocaleString()} tokens` : "not reported")}
      ${row("Tools", caps.tool_calls ? "available" : "not available")}
      ${row("Images", caps.image_input || caps.vision === "reads images" ? "available" : "not available")}
      ${row("Reasoning", caps.reasoning_control && caps.reasoning_control !== "none" ? caps.reasoning_control : "not reported")}
      ${row("Capability number", measurement ? `${measurement.passed}/${measurement.total} briefs · ${percent(measurement.tool_error_rate)} tool errors` : "unmeasured")}
    </div>
    <p class="setup-note">Measure it runs ten briefs once each and stops after five minutes. It is optional.</p>
    <div class="setup-actions">${measurement
      ? `<button data-action="capability-next">Continue</button>`
      : `<button data-action="measure" ${!connection ? "disabled" : ""}>${measuring ? "Stop" : "Measure it"}</button><button data-action="capability-next" class="quiet">Skip</button><button data-action="where" class="quiet">Back</button>`}</div>${feedback()}</section>`;
}

function doneScreen() {
  return `<section class="setup-section"><h1>Done</h1><p>Your model connection is saved.</p><div class="setup-actions"><button data-action="finish">Open Chat</button></div>${feedback()}</section>`;
}

async function click(event) {
  const button = event.target.closest("[data-action]");
  if (!button) return;
  const action = button.dataset.action;
  if (busy && !(action === "measure" && measuring)) return;
  if (action === "show-install") return showInstall();
  if (action === "save") return saveConnection();
  if (action === "query-models") return queryModels();
  if (action === "test") return testConnection();
  if (action === "install") return installModel();
  if (action === "later") return go("done");
  if (action === "where") return go("where");
  if (action === "capability-next") return go("done");
  if (action === "measure") return measuring ? stopMeasurement() : measure();
  if (action === "finish") return finish();
}

function change() {}

async function showInstall() {
  detection = {}; catalog = {}; message = ""; render();
  try {
    const values = await Promise.all([request("/api/local-detection", undefined, "GET"), request("/api/model-install", undefined, "GET")]);
    detection = values[0]; catalog = values[1].catalog; installState = values[1].state;
    const available = Math.floor(Number(localRecommendationBytes(detection)) / 2 ** 30);
    catalog.models = (catalog.models || []).filter((item) => !available || available >= item.min_gib);
  } catch (error) { fail(error); }
  render();
}

async function testConnection() {
  const url = field("url"), model = field("model"), credential = field("credential"), apiKey = field("api-key");
  if (!url) return fail(new Error("Address is required before Test."));
  setBusy("Saving and testing the connection…");
  const previousConnections = [...(snapshot.config.connections || [])];
  const previousAgents = [...(snapshot.config.agents || [])];
  let provisional = false;
  try {
    if (!connectionID || !snapshot.connections?.some((item) => item.id === connectionID)) connectionID = uniqueID("setup-model");
    const current = selectedConnection() || {};
	const previousProbe = current.capabilities?.probed_at || "";
    const connection = connectionFromFields(current, connectionID, url, model);
    if (credential) connection.credential = credential;
    if (apiKey) connection.api_key = apiKey;
    const connections = [...(snapshot.config.connections || []).filter((item) => item.id !== connectionID), connection];
    const agents = previousAgents.length ? previousAgents : [{ name: "Agent_b", b: connectionID, toolset: fullTools }];
    provisional = !previousAgents.length;
    snapshot.config = await request("/api/config", { connections, agents });
    let discovered = await request(`/api/connections/${encodeURIComponent(connectionID)}/probe`, {});
    discoveredModels = discovered.models || [];
    discoveryNote = discovered.message || "";
    if (discovered.status === "changes_required" && discovered.changes?.base_url) {
      connectionDraft = { ...connection, base_url: discovered.changes.base_url };
      discoveryNote = `${discovered.message}; Save this discovery change, then Test again.`;
      message = "Discovery change is unsaved.";
      alarm = false;
      return;
    }
    snapshot = await request("/api/state", undefined, "GET");
    render();
    if (discovered.status === "model_required") {
      message = `Test failed — ${discovered.error}`;
      alarm = true;
      return;
    }
    await waitForProbe(previousProbe);
    await assignTestedConnection();
    go("capability");
  } catch (error) {
    if (provisional) {
      try { snapshot.config = await request("/api/config", { connections: previousConnections, agents: previousAgents }); } catch {}
    }
    fail(error);
  } finally { busy = false; render(); }
}

function connectionFromFields(current, id, url = field("url"), model = field("model")) {
  const next = { ...current, id, label: field("label") || current.label || "My model", base_url: url, model };
  const credential = field("credential"), apiKey = field("api-key"), nctx = Number(field("context") || 0);
  if (credential) next.credential = credential;
  if (apiKey) next.api_key = apiKey;
  next.context = { ...(current.context || {}), n_ctx: nctx };
  next.reasoning = { ...(current.reasoning || {}), enabled: field("reasoning") !== "false" };
  return next;
}

function captureConnectionDraft() {
  const current = selectedConnection() || {};
  const id = connectionID || "setup-model";
  return connectionFromFields(current, id);
}

async function saveConnection() {
  const url = field("url");
  if (!url) return fail(new Error("Address is required before Save."));
  if (!connectionID || !snapshot.connections?.some((item) => item.id === connectionID)) connectionID = uniqueID("setup-model");
  const next = connectionFromFields(selectedConnection() || {}, connectionID);
  setBusy("Saving connection…");
  try {
    const connections = [...(snapshot.config.connections || []).filter((item) => item.id !== connectionID), next];
    snapshot.config = await request("/api/config", { connections });
    snapshot = await request("/api/state", undefined, "GET");
    connectionDraft = undefined;
    message = "Connection saved.";
    discoveryNote = "";
  } catch (error) { fail(error); } finally { busy = false; render(); }
}

async function queryModels() {
  const url = field("url");
  if (!url) return fail(new Error("Enter an address before Query models."));
  const apiKey = field("api-key");
  connectionDraft = captureConnectionDraft();
  setBusy("Querying models…");
  try {
    const result = await request(`/api/connections/${encodeURIComponent(connectionID || "setup-model")}/models`, { base_url: url, api_key: apiKey });
    discoveredModels = result.models || [];
    discoveryNote = result.message || "Models loaded.";
  } catch (error) { fail(error); } finally { busy = false; render(); }
}

async function installModel() {
  setBusy("Starting verified downloads…");
  try {
    await request("/api/model-install", { model_id: field("install-model"), backend: localBackend(detection).backend });
    while (true) {
      await delay(750);
      installState = (await request("/api/model-install", undefined, "GET")).state;
      render();
      if (!installState.running) break;
    }
    if (installState.error) throw new Error(installState.error);
    connectionID = installState.connection_id;
    snapshot = await request("/api/state", undefined, "GET");
    await waitForProbe("");
    await assignTestedConnection();
    go("capability");
  } catch (error) { fail(error); } finally { busy = false; render(); }
}

async function measure() {
  measuring = true;
  setBusy("Running ten briefs (five-minute cap)…");
  try {
    await request("/api/eval/measure", { connection_id: connectionID });
    while (true) {
      await delay(750);
      const state = await request(`/api/eval/measure?connection_id=${encodeURIComponent(connectionID)}`, undefined, "GET");
      message = state.text || "Measuring…";
      if (!state.running) {
        if (state.error) throw new Error(state.error);
        snapshot = await request("/api/state", undefined, "GET");
        measuring = false;
        busy = false;
        go("done");
        break;
      }
      render();
    }
  } catch (error) { fail(error); } finally { measuring = false; busy = false; render(); }
}

async function stopMeasurement() {
  message = "Stopping after the current brief…";
  render();
  try {
    await request(`/api/eval/measure?connection_id=${encodeURIComponent(connectionID)}`, undefined, "DELETE");
  } catch (error) { fail(error); }
}

async function assignTestedConnection() {
  const current = snapshot.config.agents?.[0] || { name: "Agent_b", toolset: fullTools };
  const agent = { ...current, name: current.name || "Agent_b", toolset: current.toolset || fullTools };
  if (!agent.b) agent.b = connectionID;
  else if (agent.b !== connectionID && !agent.c) agent.c = connectionID;
  snapshot.config = await request("/api/config", { agents: [agent] });
}

async function finish() {
  setBusy("Opening chat…");
  try {
    snapshot = await request("/api/state", undefined, "GET");
    const agent = snapshot.config.agents?.[0];
    if (!Object.keys(snapshot.sessions || {}).length && agent) {
      const agentID = agent.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
      const created = await request("/api/sessions", { label: "main", agent_id: agentID });
      location.href = `/chat?session=${encodeURIComponent(created.id || created.session?.id || "main")}`;
      return;
    }
    location.href = "/chat";
  } catch (error) { busy = false; fail(error); }
}

async function waitForProbe(previousProbe = "") {
  const deadline = Date.now() + 15 * 60 * 1000;
  while (Date.now() < deadline) {
    snapshot = await request("/api/state", undefined, "GET");
    const caps = selectedConnection()?.capabilities || {};
    if (caps.probed_at && caps.probed_at !== previousProbe) return;
    const failure = (caps.findings || []).find((item) => String(item).startsWith("probe failed:"));
    if (failure) throw new Error(failure);
    await delay(750);
  }
  throw new Error("Test did not finish before the connection timeout.");
}

function remoteGuide() {
  return `On the model computer:\n1. Download the same llama.cpp release and verify its published SHA-256.\n2. Start: llama-server -m <verified-model.gguf> --host 0.0.0.0 --port 8080\n3. Allow TCP 8080 only on your private network.\n4. Enter http://<private-host-address>:8080 above and select Test.\nAgent_b does not start, restart, or reconfigure a remote model host.`;
}

function go(next) { step = next; message = ""; alarm = false; render(); }
function setBusy(text) { busy = true; message = text; alarm = false; render(); }
function fail(error) { message = error.message || String(error); alarm = true; render(); }
function selectedConnection() { return snapshot.connections?.find((item) => item.id === connectionID); }
function field(name) { return root.querySelector(`[data-field="${name}"]`)?.value?.trim() || ""; }
function row(label, value) { return `<div><span>${html(label)}</span><strong>${html(value)}</strong></div>`; }
function feedback() { return message ? `<p class="setup-feedback ${alarm ? "alarm" : ""}" role="status">${html(message)}</p>` : ""; }
function disabled() { return busy ? "disabled" : ""; }
function localBackend(report = {}) {
  const gpus = report.gpus || [];
  if (report.accelerators?.cuda) return { backend: "cuda", gpu: gpus.find((item) => item.vendor === "NVIDIA") || gpus[0] };
  if (report.accelerators?.vulkan) return { backend: "vulkan", gpu: gpus[0] };
  return { backend: "cpu", gpu: null };
}
function localRecommendationBytes(report = {}) { return localBackend(report).gpu?.vram_bytes || report.system_memory_bytes || 0; }
function memory(bytes, kind) { return `${(Number(bytes || 0) / 2 ** 30).toFixed(1)} GiB ${kind}`; }
function percent(value) { return `${(Number(value || 0) * 100).toFixed(0)}%`; }
function uniqueID(base) { let id = base, n = 2; while (snapshot.connections?.some((item) => item.id === id)) id = `${base}-${n++}`; return id; }
function delay(ms) { return new Promise((resolve) => setTimeout(resolve, ms)); }
function link(url, label) { return /^https:\/\//.test(url || "") ? `<a href="${attr(url)}" target="_blank" rel="noreferrer">${html(label)}</a>` : html(label); }
function html(value) { return String(value ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]); }
function attr(value) { return html(value); }

async function request(path, body, method = "POST") {
  const options = { method, headers: {} };
  if (method !== "GET" && method !== "HEAD") options.headers["X-AgentB-Mutation-Token"] = snapshot?.mutation_token || "";
  if (body !== undefined) { options.headers["Content-Type"] = "application/json"; options.body = JSON.stringify(body); }
  const response = await fetch(path, options);
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(value.error || `HTTP ${response.status}`);
  return value;
}
