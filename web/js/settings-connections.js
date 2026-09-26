let expanded, advancedConnections, armed, drafts, errors, probeMessages, connectionList, row, subhead, text, number, numberControl, textarea, secret, toggle, choices, connectionReason, html, attr, store;
function useSettingsContext(context) {
  ({ expanded, advancedConnections, armed, drafts, errors, probeMessages, connectionList, row, subhead, text, number, numberControl, textarea, secret, toggle, choices, connectionReason, html, attr, store } = context);
}

function connections() {
  const connections = connectionList();
  const rows = connections
    .map((connection) => {
      const isOpen = expanded.has(connection.id);
      const hasPendingChanges = [...drafts.keys()].some((path) => path.startsWith(`connections.${connection.id}.`));
      const feedback = probeMessages.get(connection.id);
      const reason = connectionReason(connection);
      const failed = (connection.capabilities?.findings || []).some((x) =>
        x.startsWith("probe failed:"),
      );
      // Item 2l1 (a5): Test and fill proposes values, so a successful test leaves
      // unsaved changes by design. The result of the test is what the operator
      // asked for and comes first; "unsaved" is appended rather than replacing it,
      // because a bare "unsaved" hides whether the connection actually works.
      const testState = connection._probing
        ? "testing"
        : feedback?.message
          ? feedback.message + (hasPendingChanges ? " · unsaved" : "")
          : hasPendingChanges
            ? "unsaved"
            : failed
              ? "failed"
              : !reason && connection.capabilities?.probed_at
                ? "ready"
                : "not tested";
      const ready = !reason && !!connection.capabilities?.probed_at;
      const lamp = failed || feedback?.alarm || (reason && reason !== "context length unknown") ? "alarm" : connection._probing || ready || feedback ? "live" : "";
      const removeKey = `connection:${connection.id}`;
      return `<div class="connection-row ${isOpen ? "selected" : ""}">
          <button type="button" class="connection-summary" data-action="connection-toggle" data-id="${attr(connection.id)}">
            <span class="lamp ${lamp}"></span><span>${html(connection.label)}</span><span class="connection-url">${html(connection.base_url)}</span><span class="connection-state">${testState}</span>
          </button>
          <button type="button" class="connection-remove ${armed.has(removeKey) ? "confirm" : ""}" data-action="remove-connection" data-id="${attr(connection.id)}" aria-label="${armed.has(removeKey) ? `Confirm remove ${attr(connection.label)}` : `Remove ${attr(connection.label)}`}" title="${armed.has(removeKey) ? `Confirm remove ${attr(connection.label)}` : `Remove ${attr(connection.label)}`}">${armed.has(removeKey) ? "Confirm ×" : "×"}</button>
      </div>`;
    })
    .join("");
  const editors = connections.filter((connection) => expanded.has(connection.id)).map((connection) => `<section class="connection-editor" aria-label="${attr(connection.label)} connection settings">
    <div class="connection-editor-head"><div><span class="lamp ${connectionReason(connection) && connectionReason(connection) !== "context length unknown" ? "alarm" : ""}"></span><h3>${html(connection.label)}</h3><span class="connection-url">${html(connection.base_url)}</span></div></div>
    <div class="connection-fields">${connectionFields(connection, connectionReason(connection), probeMessages.get(connection.id))}</div>
  </section>`).join("");
  return `${row("actions", '<div class="settings-actions settings-connections-actions"><button type="button" data-action="open-setup">Open setup guide</button><button type="button" data-action="add-connection">Add connection</button></div>')}${subhead("Connections", "Model endpoints and their current probe state.")}<div class="connection-list">${rows || '<p class="settings-note inline">No connections configured.</p>'}</div>${editors}`;
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
function connectionFields(connection, reason, discovery) {
  const id = connection.id;
  const p = `connections.${id}`;
  const caps = connection.capabilities || {};
  const efforts = connection.reasoning?.valid_efforts || caps.valid_efforts || [];
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
  ].map(([name, label, step, disabled]) => `<div class="sampling-label">${html(label)}</div>${["thinking", "nonthinking"].map((mode) => `<div>${numberControl(`${p}.sampling.${mode}.${name}`, connection.sampling[mode][name], step, disabled)}${disabled ? '<span class="control-note">llama.cpp only</span>' : ""}</div>`).join("")}`).join("");
	const discoveredModels = discovery?.models || [];
	const modelName = (model) => String(model).split(/[\\/]/).pop();
	const picker = discoveredModels.length
	  ? `<select class="setting-input" data-path="${attr(`${p}.model`)}" data-kind="text">${!discoveredModels.includes(connection.model) && connection.model ? `<option value="${attr(connection.model)}" selected>${html(connection.model)}</option>` : ""}${discoveredModels.map((model) => `<option value="${attr(model)}" title="${attr(model)}" ${model === connection.model ? "selected" : ""}>${html(modelName(model))}</option>`).join("")}</select>`
	  : `<input class="setting-input" data-path="${attr(`${p}.model`)}" data-kind="text" value="${attr(connection.model || "")}">`;
	// Item 2l1 (a2): one action, not two. Test contacts the address once and fills
	// the picker and the connection settings from that single result.
	const modelControl = row("model", `<span class="settings-actions">${picker}</span>`, "", "Filled by Test from what the server lists; type a name by hand when a server cannot enumerate.");
	const feedback = discovery?.message ? `<p class="settings-note ${discovery.alarm ? "alarm" : ""}">${html(discovery.message)}</p>` : "";
	const measurement = connection.measurement;
	const measurementResult = measurement ? `<div class="findings"><span class="settings-note">${html(measurement.measured_at || "measured")}</span><ul><li>${Number(measurement.passed || 0)}/${Number(measurement.total || 10)} passed</li><li>${Number(measurement.tool_errors || 0)} tool errors</li></ul></div>` : "";
	const state = reason || (caps.probed_at ? "ready" : "not tested");
	return `<div class="connection-fieldset connection-identity">${text(`${p}.label`, "label", connection.label, "text", "The name this connection is shown by.")}
    ${text(`${p}.base_url`, "base_url", connection.base_url, "text", "The server address; Test and fill discovers its API path and port, lists models and proposes the rest.")}${discovery?.base_url ? `<p class="settings-note discovery-note">${html(discovery.found || `found ${discovery.base_url}`)}</p>` : ""}
	${text(`${p}.credential`, "credential ref", connection.credential || "", "text", "The name the stored API key is kept under; the key itself is never in the configuration.")}
    ${secret(`${p}.api_key`, "api_key", connection.api_key, id, "API keys are stored in user-scoped DPAPI storage; configuration keeps only the credential reference.")}
    ${modelControl}
    ${row("context size", `<input class="setting-input number" type="number" step="1" data-path="${attr(`${p}.context.n_ctx`)}" data-kind="number" value="${attr(connection.context.n_ctx || "")}" placeholder="${attr(caps.n_ctx || "")}">`, "", "The probed context is used as the placeholder until this is saved.")}
    ${toggle(`${p}.reasoning.enabled`, "enabled", connection.reasoning.enabled, "Asks the model to think before it answers, where the server supports it.")}
    ${row("state", `<span class="account-status"><span class="lamp ${reason && reason !== "context length unknown" ? "alarm" : ""}"></span>${html(state)}</span>`)}
    <div class="settings-actions"><button type="button" data-action="probe" data-id="${attr(id)}" ${connection._probing ? "disabled" : ""}>${connection._probing ? "Testing…" : "Test and fill"}</button><button type="button" data-action="measure-connection" data-id="${attr(id)}">${discovery?.measureRunning ? "Stop" : "Evaluation Harness"}</button><button type="button" data-action="duplicate-connection" data-id="${attr(id)}">Duplicate</button></div>${feedback}${measurementResult}</div>
    <details class="connection-advanced" data-connection-advanced="${attr(id)}" ${advancedConnections.has(id) ? "open" : ""}><summary>Advanced</summary>
    <div class="connection-fieldset connection-identity"><h4>Connection</h4>
	${text(`${p}.extract_url`, "extract_url", connection.extract_url || "", "text", "An optional service that turns PDFs into text for this connection; it is used before the local reader.")}
	${choices(`${p}.attachment_handling`, "attachment handling", ["auto", "native", "extract"], connection.attachment_handling || "auto", "auto follows probed capability; native always sends supported attachment kinds; extract keeps their binary local")}
	${number(`${p}.request_timeout_s`, "timeout", connection.request_timeout_s, "1", false, "", false, "number", "Seconds to wait for the server before a request counts as failed.")}
    ${choices(`${p}.probe_mode`, "probe mode", ["full", "minimal", "off"], connection.probe_mode, "minimal and off skip checks that spend tokens; assumed values are marked in findings")}</div>
    <div class="connection-fieldset connection-reasoning"><h4>Reasoning &amp; context</h4>
    ${choices(`${p}.reasoning.control`, "control", ["auto", "chat_template_kwargs", "top_level", "server_flag", "none"], connection.reasoning.control, "How the thinking switch is sent to this server; auto uses what the probe found.")}
    ${efforts.length ? choices(`${p}.reasoning.effort`, "effort", efforts, connection.reasoning.effort, "How much the model thinks before it answers.") : row("effort", '<span class="settings-note inline">not supported by this server</span>', "", "This server offers no thinking levels to choose from.")}
    ${toggle(`${p}.reasoning.preserve`, "preserve", connection.reasoning.preserve, "Sends the model's own earlier reasoning back to it within a run.")}
    ${number(`${p}.reasoning.max_tokens`, "reasoning cap", connection.reasoning.max_tokens || 0, "1", false, "", false, "number", "The most tokens the model may spend thinking per answer; 0 means no cap.")}
    ${number(`${p}.context.reserve_output`, "reserve", connection.context.reserve_output, "1", false, "", false, "number", "Tokens kept free for one answer, thinking included. An answer that reaches this limit stops and says so; thinking gets half of it when no reasoning cap is set. It cannot exceed half the context size.")}</div>
    <div class="connection-fieldset connection-sampling"><h4>Sampling</h4><div class="sampling-grid"><div></div><div class="sampling-column">Thinking</div><div class="sampling-column">Non-thinking</div>${samplingRows}</div></div>
    <div class="connection-fieldset connection-prompt"><h4>System prompt</h4>
    ${textarea(`${p}.system_prompt_override`, "system prompt override", connection.system_prompt_override || "", "variables: {{folder}} {{plans}} {{tools}} {{agent}} {{project}} {{memory}}")}</div>
    <div class="connection-fieldset connection-capabilities"><h4>Capabilities</h4>
    <div class="findings"><span class="settings-note">${html(caps.probed_at || "not probed")}</span><ul>${findings || "<li>no findings</li>"}</ul></div>
    ${reason && reason !== "context length unknown" ? `<p class="field-error">${html(reason)}</p>` : ""}
    ${errors.get(p) ? `<p class="field-error">${html(errors.get(p))}</p>` : ""}</div></details>`;
}


export function renderConnectionsPage(context) {
  useSettingsContext(context);
  return connections();
}
