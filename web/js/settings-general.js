let advancedConnections, store, armed, connectionList, row, text, number, toggle, copyRow, issue, connectionReason, html, attr, errorMarkup, errors;
function useSettingsContext(context) {
  ({ advancedConnections, store, armed, connectionList, row, text, number, toggle, copyRow, issue, connectionReason, html, attr, errorMarkup, errors } = context);
}

function sessions() {
  const connections = connectionList().filter((connection) => !connectionReason(connection));
  const items = Object.values(store.sessions)
    .map((item) => {
      const running = item.run.status !== "idle";
      const key = `session:${item.id}`;
      const connectionOptions = connectionList()
        .map((candidate) => {
          const problem = connectionReason(candidate);
          return `<option value="${attr(candidate.id)}" ${candidate.id === item.connection_id ? "selected" : ""} ${problem ? "disabled" : ""}>${html(candidate.label)}</option>`;
        })
        .join("");
      return `<div class="session-row">
        <input class="session-label" data-session-label="${attr(item.id)}" value="${attr(item.label)}" aria-label="${attr(item.id)} label">
        <select data-session-connection="${attr(item.id)}" aria-label="${attr(item.id)} server" ${running || store.replay ? "disabled" : ""}>${connectionOptions}</select><span class="path" title="${attr(item.workspace)}">${html(item.workspace)}</span>
        <span>${html(item.run.status)}</span>
        <button type="button" data-action="close-session" data-id="${attr(item.id)}" data-confirm="chat ${attr(item.label)}">Close</button>
      </div>${issue(`session.${item.id}`) ? errorMarkup(issue(`session.${item.id}`), `session:${item.id}`) : ""}`;
    })
    .join("");
  const defaultConnection = store.config.agents?.[0]?.b || "";
  const options = connections
    .map((p) => `<option value="${attr(p.id)}" ${p.id === defaultConnection ? "selected" : ""}>${html(p.label)}</option>`)
    .join("");
  return `${items}
    <div class="settings-subhead">New session</div>
    ${row("label", '<input id="new-session-label" value="new session">', "", "The name a new session starts with.")}
    ${row("connection", `<select id="new-session-connection">${options}</select>`, "", "The model connection a new session runs on.")}
    <button type="button" class="text-action" data-action="new-session" ${options ? "" : "disabled"}>New session</button>
    ${issue("new-session") ? errorMarkup(issue("new-session"), "new-session") : ""}
    `;
}

function tools(active) {
  const toolState = Object.fromEntries((active?.tools || []).map((tool) => [tool.name, tool]));
  const tokenCount = (value) => Number(value || 0).toLocaleString("en-US");
  const head = (name) => {
    const tool = toolState[name] || {};
    return `<div class="tool-setting-head"><code>${name}</code><span class="tool-cost"><span class="tool-cost-primary">${tokenCount(tool.marginal_tokens)} marginal</span><span>schema ${tokenCount(tool.schema_tokens)}</span></span></div>${errors.get(`tools.${name}`) ? errorMarkup(errors.get(`tools.${name}`), `tool:${name}`) : ""}`;
  };
  const cfg = store.config;
  const availability = active
    ? `<div class="settings-subhead">Current session</div>${active.tools.map((tool) => row(tool.name, `<span class="session-tool-control"><span class="number">${tool.calls || 0} calls</span><button class="switch ${tool.enabled ? "on" : ""}" type="button" data-action="session-tool-toggle" data-id="${attr(tool.name)}" data-enabled="${tool.enabled}" aria-label="Toggle ${attr(tool.name)}" title="Toggle ${attr(tool.name)}" aria-pressed="${tool.enabled}"></button></span>`, "", `Turns ${tool.name} on or off for this session from its next model request; the count is its calls so far.`)).join("")}`
    : '<p class="settings-note">No active session.</p>';
  const blockTokens = active?.budget?.categories?.tools;
  const block = row("enabled tools block", `<span class="number">${blockTokens == null ? "not measured" : `${tokenCount(blockTokens)} tokens`}</span>`, "", "Marginals include the tool-name prompt; neither marginals nor schema sizes sum to the block because shared scaffolding is counted once.");
  return `${availability}<div class="settings-subhead">Configuration and request cost</div>${block}
    ${head("read_file")}
	<div class="settings-subhead">Attachment ingest</div>
    ${head("list_dir")}
    ${text("tools.list_dir.ignore", "ignore", (cfg.tools?.list_dir?.ignore || []).join(", "), "list", "Folder names list_dir skips, comma separated.")}
    ${head("write_file")}${head("edit_file")}
    ${head("search")}
    ${head("shell")}
	${text("tools.shell.operator_commands", "Run as you commands", (cfg.tools?.shell?.operator_commands || []).join(", "), "list", "Commands that always run as you rather than the service identity, comma separated.")}
    ${text("shell.deny", "deny", (cfg.shell?.deny || []).join(", "), "list", "Command fragments the shell refuses to run, comma separated.")}
    ${head("remember")}${head("recall")}${head("fetch_url")}${head("web_search")}${head("run_script")}${head("call_service")}${limits(active)}`;
}

function memory(active) {
  const value = (active?.memory_content || "")
    .split(/\r?\n/)
    .slice(0, 200)
    .join("\n");
  return `${toggle("memory.enabled", "enabled", store.config.memory?.enabled, "Loads saved notes into each chat and lets the model save new ones.")}
    ${text("memory.dir", "directory", store.config.memory?.dir || "", "text", "Where memory notes are stored.")}
    ${copyRow("file", active?.memory_path || "", "The memory file this chat loads; copy copies its path.")}
    <pre class="memory-content">${html(value || "No notes for this folder.")}</pre>`;
}

function limits(active) {
 const connection=connectionList().find(c=>c.id===active?.connection_id);
 const window=Number(active?.budget?.n_ctx || connection?.context?.n_ctx || connection?.capabilities?.n_ctx || 0);
 const fields=[
 ["tools.read_file.default_limit","default bytes",16384,true,"Bytes read_file returns when a call names no limit."],
 ["tools.read_file.max_limit","max bytes per call",65536,true,"The most bytes one read_file call may return."],
 ["tools.list_dir.max_entries","max entries",300,true,"The most entries one list_dir call returns."],
 ["tools.grep.max_matches","max matches",50,true,"The most matches one search_text call returns."],
 ["shell.max_output_lines_head","head lines",60,true,"Lines kept from the start of long shell output."],
 ["shell.max_output_lines_tail","tail lines",40,true,"Lines kept from the end of long shell output."],
 ["tools.attachments.max_bytes","max upload bytes",268435456,false,"disk; not a prompt limit"],
 ["tools.grep.max_line_chars","max line chars",200,false,"Longer matching lines are cut to this many characters."],
 ["shell.timeout_s","timeout",60,false,"Seconds a shell command may run when the call names no timeout."],
 ["shell.max_timeout_s","max timeout",600,false,"The longest timeout a shell call may ask for, in seconds."],
 ["memory.max_tokens","max tokens",16384,false,"The most tokens each memory layer may add to a chat's prompt."]
 ];
 return `<details class="settings-advanced" data-general-advanced data-settings-advanced="settings:general" ${advancedConnections.has("settings:general") ? "open" : ""}><summary>Advanced</summary>`+fields.map(([path,label,base,dynamic,hint])=>{
 const saved=path.split('.').reduce((value,key)=>value?.[key],store.config),automatic=dynamic&&(!saved||Number(saved)===base);
 const computed=Math.floor(base*Math.max(1,window/32768));
 return number(path,label,automatic?'':saved??base,'1',false,dynamic?'blank: automatic ('+computed+' at this context)':'',false,dynamic?'automatic:'+base:'number',hint);
 }).join('')+'</details>';
}

export function renderGeneralPage(page, active, pageContext) {
  useSettingsContext(pageContext);
  return ({ sessions, tools, memory, limits })[page]?.(active) || "";
}
import { actionTable } from "./settings-action-table.js";

export const generalActions = actionTable([
  "setting-menu-toggle", "error-details", "close-error-details", "close",
  "save-setting", "settings-section", "save-settings", "config-toggle",
  "config-choice", "copy",
]);
