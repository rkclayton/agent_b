// Item 2l5 (g): drawn to the sheet's weight and metrics — 13px, currentColor, no
// new colour and no new stroke width. Three of the four are icon-only, so every one
// carries an accessible label at its call site.
const connectionIcons = {
  save: '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false"><path d="M2 2h9l3 3v9H2V2Zm2 1v4h6V3H4Zm1 7h6v3H5v-3Z"/></svg>',
  duplicate: '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false"><path d="M3 4h6v6H3V4Zm1 1v4h4V5H4Zm1.5.8h1v1h-1v-1Zm2 0h1v1h-1v-1Z"/><path d="M7 2h6v6h-1.5V3.5H7V2Z"/></svg>',
  trash: '<svg viewBox="0 0 16 16" width="13" height="13" aria-hidden="true" focusable="false"><path d="M6 2h4v1h3v1H3V3h3V2Zm-2 3h8l-.7 9H4.7L4 5Zm2.2 1 .4 7h1V6H6.2Zm3.6 0H8.8v7h1l.4-7Z"/></svg>',
};

let expanded, advancedConnections, armed, drafts, errors, probeMessages, typedModels, connectionList, row, subhead, text, number, numberControl, textarea, secret, toggle, choices, connectionReason, html, attr, store;
function useSettingsContext(context) {
  ({ expanded, advancedConnections, armed, drafts, errors, probeMessages, typedModels, connectionList, row, subhead, text, number, numberControl, textarea, secret, toggle, choices, connectionReason, html, attr, store } = context);
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
      // Item 2nb (g): THE HEADER CARRIES THE STATE WORD ONLY, never the message. A
      // whole sentence here overflowed the row, and the same sentence was already
      // under the field it is about. The word is derived from the message rather than
      // being the message.
      const stateWord = (message) => {
        const text = String(message || "").toLowerCase();
        if (/wants an api key|api key/.test(text)) return "wants key";
        if (/no model|not served|lists no|0 model/.test(text)) return "no models";
        if (/^test failed|failed|refused|timed out|malformed/.test(text)) return "failed";
        if (/^test passed|ready/.test(text)) return "ready";
        return "";
      };
      // Item 2nn (c): while the test runs the state word is BLANK, because the
      // waiting element in the Test control beside it is the state — and because
      // the bare word "testing" is exactly what the operator read as no progress
      // at all. One waiting element, and it is a bar.
      const testState = connection._probing
        ? ""
        : (feedback?.message && stateWord(feedback.message))
          ? stateWord(feedback.message) + (hasPendingChanges ? " · unsaved" : "")
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
      // Item 2nc (a) and (d): A REFUSAL IS VISIBLE WHEREVER THE CLICK WAS. The server
      // has always sent the row as the field, and the handler has always kept it, but
      // only the fields inside the EXPANDED body rendered it — so pressing Remove on a
      // collapsed row left the screen unchanged and the reason unread. The row itself
      // carries it now, expanded or not.
      const refusal = errors.get(`connections.${connection.id}`) || "";
      return `<div class="connection-row ${isOpen ? "selected" : ""} ${refusal ? "refused" : ""}">
          <button type="button" class="connection-summary" data-action="connection-toggle" data-id="${attr(connection.id)}">
            <span class="lamp ${lamp}"></span><span>${html(connection.label)}</span><span class="connection-url">${html(connection.base_url)}</span><span class="connection-state">${testState}</span>
          </button>
          <span class="connection-actions">
            <button type="button" class="row-action" data-action="save-connection" data-id="${attr(connection.id)}" aria-label="Save ${attr(connection.label)}" title="Save ${attr(connection.label)}" ${hasPendingChanges ? "" : "disabled"}>${connectionIcons.save}</button>
            <button type="button" class="row-action test" data-action="probe" data-id="${attr(connection.id)}" aria-label="Test ${attr(connection.label)}" title="Test ${attr(connection.label)} and fill what it finds" ${connection._probing ? "disabled" : ""}>${connection._probing ? `<span class="probe-wait" data-probe-wait="${attr(connection.id)}"></span>` : "test"}</button>
            <button type="button" class="row-action" data-action="duplicate-connection" data-id="${attr(connection.id)}" aria-label="Duplicate ${attr(connection.label)}" title="Duplicate ${attr(connection.label)}">${connectionIcons.duplicate}</button>
            <button type="button" class="row-action" data-action="remove-connection" data-id="${attr(connection.id)}" data-confirm="${attr(connection.label)}" aria-label="Remove ${attr(connection.label)}" title="Remove ${attr(connection.label)}">${connectionIcons.trash}</button>
          </span>
          ${refusal ? `<p class="connection-refusal alarm" role="status">${html(refusal)}</p>` : ""}
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

// Item 2jf (e): the provenance is in the file, at the end of the line, in
// brackets. Reading it back is the same parse the Go side does and it is
// deliberately forgiving: a note written before this item has no brackets and
// must still list, without claiming a scope it never had.
function parseNotes(content, layer) {
  return String(content || "")
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter((line) => line.startsWith("- "))
    .map((line) => {
      let rest = line.slice(2).trim();
      const marks = {};
      const bracket = rest.match(/\s*\[([^\]]*)\]$/);
      if (bracket) {
        rest = rest.slice(0, bracket.index).trim();
        for (const field of bracket[1].split(",")) {
          const [key, ...value] = field.split(":");
          marks[key.trim()] = value.join(":").trim();
        }
      }
      const date = rest.match(/^(\d{4}-\d{2}-\d{2})\s+/);
      if (date) rest = rest.slice(date[0].length);
      return {
        text: rest,
        date: date ? date[1] : "",
        scope: marks.scope || "",
        layer,
        untrusted_in_turn: marks["untrusted-in-turn"] === "yes",
      };
    });
}
function memoryFinding() {
  const session = Object.values(store?.sessions || {})[0];
  if (!session || !session.memory_max_tokens) return [];
  const budget = count(session.memory_max_tokens);
  const agent = count(session.agent_memory_tokens || 0);
  const folder = count(session.memory_tokens || 0);
  const over = [];
  if (session.agent_memory_over_budget) over.push("agent");
  if (session.memory_over_budget) over.push("folder");
  // Item 2jf (d): the harness no longer trims on the way in — a write that
  // would exceed the budget is REFUSED and the model is told to replace a note
  // it names — so this line says full rather than "oldest notes omitted".
  const suffix = over.length ? ` · ${over.join(" and ")} full; the next write is refused until a note is replaced` : "";
  const lines = [`<li>memory: agent ${agent}/${budget} · folder ${folder}/${budget}${suffix}</li>`];
  // Item 2jf (f): the notes themselves, with their scope, date and provenance,
  // so the operator can see what the agent believes and where each belief came
  // from. PER-NOTE DELETE IS NOT HERE: it needs a control per row, and the
  // item’s own narrowing says to list it for veto and ship without it. The
  // whole-layer clear stays where it is.
  // The snapshot already carries each layer’s raw text, so the notes are parsed
  // here rather than adding a field to it: the file format IS the interface, and
  // a second representation would be a second thing to keep in step.
  const notes = [...parseNotes(session.agent_memory_content, "agent"), ...parseNotes(session.memory_content, "folder")];
  for (const note of notes.slice(0, 40)) {
    const marks = [note.scope, note.date].filter(Boolean).join(" · ");
    const beside = note.untrusted_in_turn ? " · written beside untrusted content" : "";
    lines.push(`<li class="settings-memory-note${note.untrusted_in_turn ? " invalid" : ""}">${html(note.text || "")}<span class="settings-memory-mark"> — ${html(marks)}${html(beside)}</span></li>`);
  }
  if (notes.length > 40) lines.push(`<li>… and ${count(notes.length - 40)} more note(s)</li>`);
  return lines;
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
	// Item 2nb (c): THE MODEL IS A DROPDOWN, ALWAYS. It used to be a text box until
	// Test had listed something, which invited the operator to type a name nobody had
	// checked — and the literal placeholder `model` that a new connection carried was
	// exactly such a name, saved and then refused elsewhere. Empty now says what to do
	// instead, and a server that cannot enumerate is one explicit choice away.
	const typedByHand = typedModels?.has(id);
	const savedModel = (connection.model || "").trim();
	const options = [];
	if (!discoveredModels.length && !savedModel) {
		options.push(`<option value="" selected>Test to list models</option>`);
	} else if (savedModel && !discoveredModels.includes(savedModel)) {
		options.push(`<option value="${attr(savedModel)}" selected>${html(modelName(savedModel))}</option>`);
	}
	// The first entry is preselected when nothing is saved yet, so Test leaves a
	// usable choice rather than an empty field.
	discoveredModels.forEach((model, index) => {
		const chosen = savedModel ? model === savedModel : index === 0;
		options.push(`<option value="${attr(model)}" title="${attr(model)}" ${chosen ? "selected" : ""}>${html(modelName(model))}</option>`);
	});
	options.push(`<option value="__type__">type a name…</option>`);
	const picker = typedByHand
	  ? `<input class="setting-input" data-path="${attr(`${p}.model`)}" data-kind="text" value="${attr(connection.model || "")}" placeholder="the model name this server expects">`
	  : `<select class="setting-input" data-path="${attr(`${p}.model`)}" data-kind="text">${options.join("")}</select>`;
	// Item 2l1 (a2): one action, not two. Test contacts the address once and fills
	// the picker and the connection settings from that single result.
	const modelControl = row("model", `<span class="settings-actions">${picker}</span>`, "", "Filled by Test from what the server lists; type a name by hand when a server cannot enumerate.");
	// Item 2nb (g): ONE MESSAGE, ONE PLACE. The discovery result used to be rendered
	// twice — once under base_url and once beside the Evaluation Harness button — and
	// the operator saw three copies of one sentence. It belongs under the field it is
	// about, and the row header carries the state word only.
	const feedback = "";
	const measurement = connection.measurement;
	const measurementResult = measurement ? renderMeasurement(measurement) : "";
	// The one note: the result of the last Test, or what discovery found.
	const noteText = discovery?.message || (discovery?.base_url ? (discovery.found || `found ${discovery.base_url}`) : "");
	// Item 2nb (b) and (i): while the walk runs, the seat below holds item 2m4's ONE
	// waiting element, mounted by the controller. A second indicator is not built here,
	// and the note is replaced by it rather than sitting beside it.
	const walking = discovery?.walking;
	// Item 2nn (c): the bar lives in the CONTROL that was pressed now, so the field's
	// own seat is gone rather than showing a second one — 2m4's rule is one waiting
	// element, and the one the operator is looking at is the one he just clicked.
	const discoveryNote = walking
	  ? ""
	  : noteText ? `<p class="settings-note discovery-note ${discovery?.alarm ? "alarm" : ""}">${html(noteText)}</p>` : "";
	const state = reason || (caps.probed_at ? "ready" : "not tested");
	// Item 2nn (d): THE SHEET READS TOP TO BOTTOM AS THE FLOW. label, address, key,
	// Test, model, Evaluate, Save — the order an operator actually does it in. The
	// settings that only make sense once a model is chosen (the credential name it
	// was stored under, the context size, the thinking switch, the state line) are
	// not on screen until one is: before that they are questions about a connection
	// that has not been established, and the operator's report was that the sheet
	// asks too much of him at once.
	// A model PICKED counts, not only a model saved: the draft is what he has chosen,
	// and the rest of the sheet has to be there for him before he presses Save.
	const chosenModel = !!String(drafts.get(`${p}.model`) ?? connection.model ?? "").trim();
	const afterModel = chosenModel ? `${text(`${p}.credential`, "credential ref", connection.credential || "", "text", "The name the stored API key is kept under; the key itself is never in the configuration.")}
    ${row("context size", `<input class="setting-input number" type="number" step="1" data-path="${attr(`${p}.context.n_ctx`)}" data-kind="number" value="${attr(connection.context.n_ctx || "")}" placeholder="${attr(caps.n_ctx || "")}">`, "", "The probed context is used as the placeholder until this is saved.")}
    ${toggle(`${p}.reasoning.enabled`, "enabled", connection.reasoning.enabled, "Asks the model to think before it answers, where the server supports it.")}
    ${row("state", `<span class="account-status"><span class="lamp ${reason && reason !== "context length unknown" ? "alarm" : ""}"></span>${html(state)}</span>`)}
    <div class="settings-actions"><button type="button" class="${discovery?.measureRunning ? "has-wait" : ""}" data-action="measure-connection" data-id="${attr(id)}">${discovery?.measureRunning ? `Stop<span class="probe-wait" data-harness-wait="${attr(id)}"></span>` : "Evaluation Harness"}</button></div>${feedback}${measurementResult}` : "";
	return `<div class="connection-fieldset connection-identity">${text(`${p}.label`, "label", connection.label, "text", "The name this connection is shown by.")}
    ${text(`${p}.base_url`, "base_url", connection.base_url, "text", "The server address; Test and fill discovers its API path and port, lists models and proposes the rest.")}${discoveryNote}
    ${secret(`${p}.api_key`, "api_key", connection.api_key, id, "API keys are stored in user-scoped DPAPI storage; configuration keeps only the credential reference.")}
    ${row("", `<div class="settings-actions"><button type="button" class="connection-test ${connection._probing ? "has-wait" : ""}" data-action="probe" data-id="${attr(id)}" ${connection._probing ? "disabled" : ""}>${connection._probing ? `<span class="probe-wait" data-probe-wait="${attr(id)}"></span>` : "Test"}</button></div>`, "", "Contacts the address exactly as typed and lists the models it serves.")}
    ${modelControl}
    ${afterModel}
    ${row("", `<div class="settings-actions"><button type="button" data-action="save-connection" data-id="${attr(id)}">Save</button></div>`, "", "Saves every change made here; Test is never a precondition.")}</div>
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

// Item 2ih (d): the Evaluation Harness screen renders the per-arm table and the
// settings it wrote, and Connections shows the same table under the profile with
// the measurement's date. It is the same function, because they are the same
// table -- two renderings would drift apart within a release.
//
// (e): the probe's n_ctx and the window the model will actually run with are in
// that table, so the operator sees the window rather than inferring it.
//
// A measurement recorded before rel-1.22.0 has one arm and no decision, and
// renders as what it was: this shows the arms only when both are present.
export function renderMeasurement(measurement) {
  const when = html(measurement.measured_at || "measured");
  const rows = [];
  const arms = measurement.reasoning_on && measurement.reasoning_off;
  if (arms) {
    const arm = (name, side) => `<tr><td>${name}</td><td>${Number(side.passed || 0)}/${Number(side.total || 0)}</td><td>${Number(side.empty_replies || 0)}</td><td>${Number(side.tool_errors || 0)}</td><td>${Number(side.completion_ms || 0)} ms</td></tr>`;
    rows.push(`<table class="harness-arms">
      <thead><tr><th>arm</th><th>passed</th><th>empty</th><th>tool errors</th><th>p95</th></tr></thead>
      <tbody>${arm("thinking on", measurement.reasoning_on)}${arm("thinking off", measurement.reasoning_off)}</tbody>
    </table>`);
  } else {
    rows.push(`<ul><li>${Number(measurement.passed || 0)}/${Number(measurement.total || 10)} passed</li><li>${Number(measurement.tool_errors || 0)} tool errors</li></ul>`);
  }
  // (c): every write is ONE LINE, and this is where the operator reads it. A
  // switch that moved by itself with no sentence beside it is the thing this
  // exists to prevent.
  if (measurement.decision?.line) rows.push(`<p class="settings-note">${html(measurement.decision.line)}</p>`);
  if (measurement.n_ctx) {
    rows.push(`<p class="settings-note">window ${Number(measurement.window_tokens || 0).toLocaleString()} of ${Number(measurement.n_ctx).toLocaleString()} tokens</p>`);
  }
  return `<div class="findings"><span class="settings-note">${when}</span>${rows.join("")}</div>`;
}
