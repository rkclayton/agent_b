const root = document.getElementById("setup");
const connection = document.getElementById("connection");
const fullTools = ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "run_script", "call_service"];
let snapshot;
let step = "where";
let profileID = "";
let detection;
let catalog;
let installState;
let busy = false;
let message = "";
let alarm = false;

root.addEventListener("click", click);
root.addEventListener("change", change);
void load();

async function load() {
  try {
    snapshot = await request("/api/state", undefined, "GET");
    profileID = snapshot.config?.agents?.[0]?.b || snapshot.servers?.[0]?.id || "";
    render();
  } catch (error) {
    connection.textContent = error.message;
    connection.className = "alarm";
  }
}

function render() {
  if (!snapshot) return;
  const names = { where: "Where is your model?", capability: "Evaluation Harness", roles: "Who does what?", done: "Done" };
  document.getElementById("setup-step").textContent = names[step];
  root.innerHTML = step === "where" ? whereScreen() : step === "capability" ? capabilityScreen() : step === "roles" ? rolesScreen() : doneScreen();
}

function whereScreen() {
  const profile = selectedProfile();
  return `<section class="setup-section"><h1>Where is your model?</h1>
    <div class="setup-fields">
      <label>Address<input data-field="url" value="${attr(profile?.base_url || "http://127.0.0.1:8080")}" placeholder="http://host:port"></label>
      <label>Model<input data-field="model" value="${attr(profile?.model || "")}" placeholder="model name"></label>
      <label>Saved credential name<input data-field="credential" value="${attr(profile?.credential || "")}" placeholder="optional"></label>
      <label>API key<input data-field="api-key" type="password" autocomplete="off" placeholder="optional"></label>
    </div>
    <div class="setup-actions"><button data-action="test" ${disabled()}>Test</button><button data-action="show-install" class="quiet">Install one here</button><button data-action="later" class="quiet">Later</button></div>
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
  const profile = selectedProfile();
  const caps = profile?.capabilities || {};
  const measurement = profile?.measurement;
  return `<section class="setup-section"><h1>Evaluation Harness</h1>
    <div class="setup-readout">
      ${row("Context", caps.n_ctx ? `${Number(caps.n_ctx).toLocaleString()} tokens` : "not reported")}
      ${row("Tools", caps.tool_calls ? "available" : "not available")}
      ${row("Images", caps.image_input || caps.vision === "reads images" ? "available" : "not available")}
      ${row("Reasoning", caps.reasoning_control && caps.reasoning_control !== "none" ? caps.reasoning_control : "not reported")}
      ${row("Capability number", measurement ? `${measurement.passed}/${measurement.total} briefs · ${percent(measurement.tool_error_rate)} tool errors` : "unmeasured")}
    </div>
    <p class="setup-note">Measure it runs ten briefs once each and stops after five minutes. It is optional.</p>
    <div class="setup-actions"><button data-action="measure" ${disabled() || !profile ? "disabled" : ""}>Measure it</button><button data-action="capability-next" class="quiet">Skip</button><button data-action="where" class="quiet">Back</button></div>${feedback()}</section>`;
}

function rolesScreen() {
  const profiles = snapshot.servers || [];
  const agent = snapshot.config?.agents?.[0] || {};
  const options = (selected) => profiles.map((item) => `<option value="${attr(item.id)}" ${item.id === selected ? "selected" : ""}>${html(item.label || item.id)}</option>`).join("");
  return `<section class="setup-section"><h1>Who does what?</h1>
    <div class="setup-fields setup-roles">
      <label>b · chat<select data-role="b">${options(agent.b || profileID)}</select></label>
      <label>c · worker<select data-role="c"><option value="">unassigned</option>${options(agent.c)}</select></label>
      <label>d · planner<select data-role="d"><option value="">unassigned</option>${options(agent.d)}</select></label>
    </div>
    <div class="setup-actions"><button data-action="assign-all" ${profiles.length ? "" : "disabled"}>Use profile for everything</button><button data-action="save-roles" ${profiles.length ? "" : "disabled"}>Save assignments</button><button data-action="done" class="quiet">Later</button></div>${feedback()}</section>`;
}

function doneScreen() {
  return `<section class="setup-section"><h1>Done</h1><p>Your model connection and role assignments are saved.</p><div class="setup-actions"><button data-action="finish">Open Chat</button></div>${feedback()}</section>`;
}

async function click(event) {
  const button = event.target.closest("[data-action]");
  if (!button || busy) return;
  const action = button.dataset.action;
  if (action === "show-install") return showInstall();
  if (action === "test") return testConnection();
  if (action === "install") return installModel();
  if (action === "later") return go("roles");
  if (action === "where") return go("where");
  if (action === "capability-next") return go("roles");
  if (action === "measure") return measure();
  if (action === "assign-all") return assignAll();
  if (action === "save-roles") return saveRoles();
  if (action === "done") return go("done");
  if (action === "finish") return finish();
}

function change(event) {
  if (event.target.matches("[data-role='b']")) profileID = event.target.value;
}

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
  if (!url || !model) return fail(new Error("Address and model are required before Test."));
  setBusy("Saving and testing the connection…");
  try {
    if (!profileID || !snapshot.servers?.some((item) => item.id === profileID)) profileID = uniqueID("setup-model");
    const current = selectedProfile() || {};
    const profile = { ...current, id: profileID, label: current.label || "My model", base_url: url, model };
    if (credential) profile.credential = credential;
    if (apiKey) profile.api_key = apiKey;
    const servers = [...(snapshot.config.servers || []).filter((item) => item.id !== profileID), profile];
    const existingAgents = snapshot.config.agents || [];
    const agents = existingAgents.length ? existingAgents : [{ name: "Agent_b", b: profileID, toolset: fullTools }];
    snapshot.config = await request("/api/config", { servers, agents });
    await request(`/api/servers/${encodeURIComponent(profileID)}/probe`, {});
    await waitForProbe();
    go("capability");
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
    profileID = installState.profile_id;
    snapshot = await request("/api/state", undefined, "GET");
    await waitForProbe();
    go("capability");
  } catch (error) { fail(error); } finally { busy = false; render(); }
}

async function measure() {
  setBusy("Running ten briefs (five-minute cap)…");
  try {
    await request("/api/eval/measure", { profile_id: profileID });
    while (true) {
      await delay(750);
      const state = await request(`/api/eval/measure?profile_id=${encodeURIComponent(profileID)}`, undefined, "GET");
      message = state.text || "Measuring…";
      if (!state.running) {
        if (state.error) throw new Error(state.error);
        snapshot = await request("/api/state", undefined, "GET");
        message = "Measurement stored on this profile.";
        break;
      }
      render();
    }
  } catch (error) { fail(error); } finally { busy = false; render(); }
}

async function assignAll() {
  const select = root.querySelector("[data-role='b']");
  const id = select?.value || profileID || snapshot.servers?.[0]?.id;
  root.querySelectorAll("[data-role]").forEach((item) => { item.value = id; });
  await saveRoles();
}

async function saveRoles() {
  const current = snapshot.config.agents?.[0] || {};
  const assignments = { b: role("b"), c: role("c"), d: role("d") };
  setBusy("Saving role assignments…");
  try {
    const agent = { ...current, name: current.name || "Agent_b", ...assignments, toolset: current.toolset || fullTools };
    snapshot.config = await request("/api/config", { agents: [agent] });
    go("done");
  } catch (error) { fail(error); } finally { busy = false; render(); }
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

async function waitForProbe() {
  const deadline = Date.now() + 15 * 60 * 1000;
  while (Date.now() < deadline) {
    snapshot = await request("/api/state", undefined, "GET");
    const caps = selectedProfile()?.capabilities || {};
    if (caps.probed_at) return;
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
function selectedProfile() { return snapshot.servers?.find((item) => item.id === profileID); }
function field(name) { return root.querySelector(`[data-field="${name}"]`)?.value?.trim() || ""; }
function role(name) { return root.querySelector(`[data-role="${name}"]`)?.value || ""; }
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
function uniqueID(base) { let id = base, n = 2; while (snapshot.servers?.some((item) => item.id === id)) id = `${base}-${n++}`; return id; }
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
