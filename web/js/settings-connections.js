let expanded, armed, drafts, errors, probeMessages, serverProfiles, row, text, number, numberControl, textarea, secret, toggle, choices, profileReason, html, attr, store;
function useSettingsContext(context) {
  ({ expanded, armed, drafts, errors, probeMessages, serverProfiles, row, text, number, numberControl, textarea, secret, toggle, choices, profileReason, html, attr, store } = context);
}

function servers() {
  const profiles = serverProfiles();
  const rows = profiles
    .map((profile) => {
      const isOpen = expanded.has(profile.id);
      const hasPendingChanges = [...drafts.keys()].some((path) => path.startsWith(`servers.${profile.id}.`));
      const feedback = probeMessages.get(profile.id);
      const reason = profileReason(profile);
      const failed = (profile.capabilities?.findings || []).some((x) =>
        x.startsWith("probe failed:"),
      );
      const testState = profile._probing
        ? "testing"
        : hasPendingChanges
          ? "unsaved"
          : feedback?.message
            ? feedback.message
            : failed
              ? "failed"
              : !reason && profile.capabilities?.probed_at
                ? "ready"
                : "not tested";
      const ready = !reason && !!profile.capabilities?.probed_at;
      const lamp = failed || feedback?.alarm || (reason && reason !== "context length unknown") ? "alarm" : profile._probing || ready || feedback ? "live" : "";
      const removeKey = `server:${profile.id}`;
      return `<div class="profile-row ${isOpen ? "selected" : ""}">
          <button type="button" class="profile-summary" data-action="profile-toggle" data-id="${attr(profile.id)}">
            <span class="lamp ${lamp}"></span><span>${html(profile.label)}</span><span class="profile-url">${html(profile.base_url)}</span><span class="profile-state">${testState}</span>
          </button>
          <button type="button" data-action="probe" data-id="${attr(profile.id)}" title="${hasPendingChanges ? "Test these unsaved connection values without saving them." : ""}" ${profile._probing ? "disabled" : ""}>${profile._probing ? "Testing…" : "Test"}</button>
          <button type="button" class="profile-remove ${armed.has(removeKey) ? "confirm" : ""}" data-action="remove-server" data-id="${attr(profile.id)}" aria-label="${armed.has(removeKey) ? `Confirm remove ${attr(profile.label)}` : `Remove ${attr(profile.label)}`}" title="${armed.has(removeKey) ? `Confirm remove ${attr(profile.label)}` : `Remove ${attr(profile.label)}`}">${armed.has(removeKey) ? "Confirm ×" : "×"}</button>
      </div>`;
    })
    .join("");
  const editors = profiles.filter((profile) => expanded.has(profile.id)).map((profile) => `<section class="profile-editor" aria-label="${attr(profile.label)} connection settings">
    <div class="profile-editor-head"><div><span class="lamp ${profileReason(profile) && profileReason(profile) !== "context length unknown" ? "alarm" : ""}"></span><h3>${html(profile.label)}</h3><span class="profile-url">${html(profile.base_url)}</span></div><button type="button" data-action="duplicate-server" data-id="${attr(profile.id)}">Duplicate</button></div>
    <div class="profile-fields">${profileFields(profile, profileReason(profile), probeMessages.get(profile.id))}</div>
  </section>`).join("");
  return `<div class="settings-actions settings-connections-actions"><button type="button" data-action="open-setup">Open setup guide</button><button type="button" data-action="add-server">Add connection</button></div><div class="settings-subhead">Profiles</div><div class="profile-list">${rows || '<p class="settings-note inline">No connections configured.</p>'}</div>${editors}`;
}

// One line on the existing findings row: each memory layer's current size
// against its budget, so the operator sees a layer filling up before the
// injection starts dropping its oldest notes. No control, no new row.
const count = (value) => new Intl.NumberFormat().format(Number(value) || 0);

function memoryFinding() {
  const session = Object.values(store?.sessions || {})[0];
  if (!session || !session.memory_max_tokens) return [];
  const budget = count(session.memory_max_tokens);
  const agent = count(session.agent_memory_tokens || 0);
  const folder = count(session.memory_tokens || 0);
  const over = [];
  if (session.agent_memory_over_budget) over.push("agent");
  if (session.memory_over_budget) over.push("folder");
  const suffix = over.length ? ` · ${over.join(" and ")} over budget, oldest notes omitted` : "";
  return [`<li>memory: agent ${agent}/${budget} · folder ${folder}/${budget}${suffix}</li>`];
}
function profileFields(profile, reason, discovery) {
  const id = profile.id;
  const p = `servers.${id}`;
  const caps = profile.capabilities || {};
  const efforts = profile.reasoning?.valid_efforts || caps.valid_efforts || [];
  const llama = caps.server === "llama.cpp";
  const findings = (caps.findings || [])
    .map((value) => `<li>${html(value)}</li>`)
    .concat(memoryFinding())
    .join("");
  const samplingRows = [
    ["temperature", "temperature", "0.01", false],
    ["top_p", "top_p", "0.01", false],
    ["top_k", "top_k", "1", !llama],
    ["min_p", "min_p", "0.01", !llama],
    ["presence_penalty", "presence penalty", "0.1", false],
    ["repeat_penalty", "repeat penalty", "0.1", !llama],
  ].map(([name, label, step, disabled]) => `<div class="sampling-label">${html(label)}</div>${["thinking", "nonthinking"].map((mode) => `<div>${numberControl(`${p}.sampling.${mode}.${name}`, profile.sampling[mode][name], step, disabled)}${disabled ? '<span class="control-note">llama.cpp only</span>' : ""}</div>`).join("")}`).join("");
	const discoveredModels = discovery?.models || [];
	const modelName = (model) => String(model).split(/[\\/]/).pop();
	const modelControl = discoveredModels.length
	  ? row("model", `<select class="setting-input" data-path="${attr(`${p}.model`)}" data-kind="text">${!discoveredModels.includes(profile.model) && profile.model ? `<option value="${attr(profile.model)}" selected>${html(profile.model)}</option>` : ""}${discoveredModels.map((model) => `<option value="${attr(model)}" title="${attr(model)}" ${model === profile.model ? "selected" : ""}>${html(modelName(model))}</option>`).join("")}</select>`, "", "Models reported by the discovered endpoint; full paths appear as option detail.")
	  : text(`${p}.model`, "model", profile.model, "text", "The model name sent with each request.");
	return `<div class="profile-fieldset profile-identity"><h4>Connection</h4>${text(`${p}.label`, "label", profile.label, "text", "The name this connection is shown by.")}
    ${text(`${p}.base_url`, "base_url", profile.base_url, "text", "The server address; Test can discover its API path and port.")}${discovery?.base_url ? `<p class="settings-note discovery-note">${html(discovery.found || `found ${discovery.base_url}`)}</p>` : ""}
	${text(`${p}.extract_url`, "extract_url", profile.extract_url || "", "text", "An optional service that turns PDFs into text for this connection; it is used before the local reader.")}
	${choices(`${p}.attachment_handling`, "attachment handling", ["auto", "native", "extract"], profile.attachment_handling || "auto", "auto follows probed capability; native always sends supported attachment kinds; extract keeps their binary local")}
    ${modelControl}
	${text(`${p}.credential`, "credential ref", profile.credential || "", "text", "The name the stored API key is kept under; the key itself is never in the configuration.")}
    ${secret(`${p}.api_key`, "api_key", profile.api_key, id, "API keys are stored in user-scoped DPAPI storage; configuration keeps only the credential reference.")}
	${number(`${p}.request_timeout_s`, "timeout", profile.request_timeout_s, "1", false, "", false, "number", "Seconds to wait for the server before a request counts as failed.")}
    ${choices(`${p}.probe_mode`, "probe mode", ["full", "minimal", "off"], profile.probe_mode, "minimal and off skip checks that spend tokens; assumed values are marked in findings")}</div>
    <div class="profile-fieldset profile-reasoning"><h4>Reasoning &amp; context</h4>
    ${choices(`${p}.reasoning.control`, "control", ["auto", "chat_template_kwargs", "top_level", "server_flag", "none"], profile.reasoning.control, "How the thinking switch is sent to this server; auto uses what the probe found.")}
    ${toggle(`${p}.reasoning.enabled`, "enabled", profile.reasoning.enabled, "Asks the model to think before it answers, where the server supports it.")}
    ${efforts.length ? choices(`${p}.reasoning.effort`, "effort", efforts, profile.reasoning.effort, "How much the model thinks before it answers.") : row("effort", '<span class="settings-note inline">not supported by this server</span>', "", "This server offers no thinking levels to choose from.")}
    ${toggle(`${p}.reasoning.preserve`, "preserve", profile.reasoning.preserve, "Sends the model's own earlier reasoning back to it within a run.")}
    ${number(`${p}.reasoning.max_tokens`, "reasoning cap", profile.reasoning.max_tokens || 0, "1", false, "", false, "number", "The most tokens the model may spend thinking per answer; 0 means no cap.")}
    ${number(`${p}.context.reserve_output`, "reserve", profile.context.reserve_output, "1", false, "", false, "number", "Tokens kept free for the model's answer.")}
	${number(`${p}.context.n_ctx`, "context size", profile.context.n_ctx, "1", false, "", false, "number", "Test fills this from the server when available. Otherwise enter the server's configured context window; it is required for use and for probe mode off.")}</div>
    <div class="profile-fieldset profile-sampling"><h4>Sampling</h4><div class="sampling-grid"><div></div><div class="sampling-column">Thinking</div><div class="sampling-column">Non-thinking</div>${samplingRows}</div></div>
    <div class="profile-fieldset profile-prompt"><h4>System prompt</h4>
    ${textarea(`${p}.system_prompt_override`, "system prompt override", profile.system_prompt_override || "", "variables: {{folder}} {{plans}} {{tools}} {{agent}} {{project}} {{memory}}")}</div>
    <div class="profile-fieldset profile-capabilities"><h4>Capabilities</h4>
    <div class="findings"><span class="settings-note">${html(caps.probed_at || "not probed")}</span><ul>${findings || "<li>no findings</li>"}</ul></div>
    ${reason && reason !== "context length unknown" ? `<p class="field-error">${html(reason)}</p>` : ""}
    ${errors.get(p) ? `<p class="field-error">${html(errors.get(p))}</p>` : ""}</div>`;
}


export function renderConnectionsPage(context) {
  useSettingsContext(context);
  return servers();
}
