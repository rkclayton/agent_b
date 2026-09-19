let store, armed, serverProfiles, row, text, number, toggle, copyRow, issue, profileReason, html, attr;
function useSettingsContext(context) {
  ({ store, armed, serverProfiles, row, text, number, toggle, copyRow, issue, profileReason, html, attr } = context);
}

function sessions() {
  const profiles = serverProfiles().filter((profile) => !profileReason(profile));
  const items = Object.values(store.sessions)
    .map((item) => {
      const running = item.run.status !== "idle";
      const key = `session:${item.id}`;
      const profileOptions = serverProfiles()
        .map((candidate) => {
          const problem = profileReason(candidate);
          return `<option value="${attr(candidate.id)}" ${candidate.id === item.server_id ? "selected" : ""} ${problem ? "disabled" : ""}>${html(candidate.label)}</option>`;
        })
        .join("");
      return `<div class="session-row">
        <input class="session-label" data-session-label="${attr(item.id)}" value="${attr(item.label)}" aria-label="${attr(item.id)} label">
        <select data-session-server="${attr(item.id)}" aria-label="${attr(item.id)} server" ${running || store.replay ? "disabled" : ""}>${profileOptions}</select><span class="path" title="${attr(item.workspace)}">${html(item.workspace)}</span>
        <span>${html(item.run.status)}</span>
        <button type="button" class="${armed.has(key) ? "confirm" : ""}" data-action="close-session" data-id="${attr(item.id)}">${running && armed.has(key) ? "Confirm" : "Close"}</button>
      </div>${issue(`session.${item.id}`) ? `<p class="field-error">${html(issue(`session.${item.id}`))}</p>` : ""}`;
    })
    .join("");
  const defaultProfile = store.config.agents?.[0]?.b || "";
  const options = profiles
    .map((p) => `<option value="${attr(p.id)}" ${p.id === defaultProfile ? "selected" : ""}>${html(p.label)}</option>`)
    .join("");
  return `${items}
    <div class="settings-subhead">New session</div>
    ${row("label", '<input id="new-session-label" value="new session">', "", "The name a new session starts with.")}
    ${row("profile", `<select id="new-session-profile">${options}</select>`, "", "The model profile a new session runs on.")}
    <button type="button" class="text-action" data-action="new-session" ${options ? "" : "disabled"}>New session</button>
    ${issue("new-session") ? `<p class="field-error">${html(issue("new-session"))}</p>` : ""}
    ${number("run.max_concurrent", "max concurrent", store.config.run?.max_concurrent, "1", false, "", false, "number", "How many chats may run at the same time; the rest wait in the queue.")}`;
}

function tools(active) {
  const toolState = Object.fromEntries((active?.tools || []).map((tool) => [tool.name, tool]));
  const tokenCount = (value) => Number(value || 0).toLocaleString("en-US");
  const head = (name) => {
    const tool = toolState[name] || {};
    return `<div class="tool-setting-head"><code>${name}</code><span class="tool-cost"><span class="tool-cost-primary">${tokenCount(tool.marginal_tokens)} marginal</span><span>schema ${tokenCount(tool.schema_tokens)}</span></span></div>`;
  };
  const cfg = store.config;
  const availability = active
    ? `<div class="settings-subhead">Current session</div>${active.tools.map((tool) => row(tool.name, `<span class="session-tool-control"><span class="number">${tool.calls || 0} calls</span><button class="switch ${tool.enabled ? "on" : ""}" type="button" data-action="session-tool-toggle" data-id="${attr(tool.name)}" data-enabled="${tool.enabled}" aria-label="Toggle ${attr(tool.name)}" title="Toggle ${attr(tool.name)}" aria-pressed="${tool.enabled}"></button></span>`, "", `Turns ${tool.name} on or off for this session from its next model request; the count is its calls so far.`)).join("")}`
    : '<p class="settings-note">No active session.</p>';
  const blockTokens = active?.budget?.categories?.tools;
  const block = row("enabled tools block", `<span class="number">${blockTokens == null ? "not measured" : `${tokenCount(blockTokens)} tokens`}</span>`, "", "Marginals include the tool-name prompt; neither marginals nor schema sizes sum to the block because shared scaffolding is counted once.");
  return `${availability}<div class="settings-subhead">Configuration and request cost</div>${block}
    ${head("read_file")}
    ${number("tools.read_file.default_limit", "default bytes", cfg.tools?.read_file?.default_limit, "1", false, "", false, "number", "Bytes read_file returns when a call names no limit.")}
    ${number("tools.read_file.max_limit", "max bytes per call", cfg.tools?.read_file?.max_limit, "1", false, "", false, "number", "The most bytes one read_file call may return.")}
	<div class="settings-subhead">Attachment ingest</div>
	${number("tools.attachments.max_bytes", "max upload bytes", cfg.tools?.attachments?.max_bytes, "1", false, "", false, "number", "The largest file you can attach to a chat.")}
    ${head("list_dir")}
    ${number("tools.list_dir.max_entries", "max entries", cfg.tools?.list_dir?.max_entries, "1", false, "", false, "number", "The most entries one list_dir call returns.")}
    ${text("tools.list_dir.ignore", "ignore", (cfg.tools?.list_dir?.ignore || []).join(", "), "list", "Folder names list_dir skips, comma separated.")}
    ${head("write_file")}${head("edit_file")}
    ${head("search_text")}
    ${number("tools.grep.max_matches", "max matches", cfg.tools?.grep?.max_matches, "1", false, "", false, "number", "The most matches one search_text call returns.")}
    ${number("tools.grep.max_line_chars", "max line chars", cfg.tools?.grep?.max_line_chars, "1", false, "", false, "number", "Longer matching lines are cut to this many characters.")}
    ${head("shell")}
	${text("tools.shell.operator_commands", "operator commands", (cfg.tools?.shell?.operator_commands || []).join(", "), "list", "Commands that always run as you rather than the service identity, comma separated.")}
    ${number("shell.timeout_s", "timeout", cfg.shell?.timeout_s, "1", false, "", false, "number", "Seconds a shell command may run when the call names no timeout.")}
    ${number("shell.max_timeout_s", "max timeout", cfg.shell?.max_timeout_s, "1", false, "", false, "number", "The longest timeout a shell call may ask for, in seconds.")}
    ${number("shell.max_output_lines_head", "head lines", cfg.shell?.max_output_lines_head, "1", false, "", false, "number", "Lines kept from the start of long shell output.")}
    ${number("shell.max_output_lines_tail", "tail lines", cfg.shell?.max_output_lines_tail, "1", false, "", false, "number", "Lines kept from the end of long shell output.")}
    ${text("shell.deny", "deny", (cfg.shell?.deny || []).join(", "), "list", "Command fragments the shell refuses to run, comma separated.")}
    ${head("remember")}${head("recall")}${head("fetch_url")}${head("find_files")}${head("run_script")}${head("call_service")}`;
}

function memory(active) {
  const value = (active?.memory_content || "")
    .split(/\r?\n/)
    .slice(0, 200)
    .join("\n");
  return `${toggle("memory.enabled", "enabled", store.config.memory?.enabled, "Loads saved notes into each chat and lets the model save new ones.")}
    ${number("memory.max_tokens", "max tokens", store.config.memory?.max_tokens, "1", false, "", false, "number", "The most tokens each memory layer may add to a chat's prompt.")}
    ${text("memory.dir", "directory", store.config.memory?.dir || "", "text", "Where memory notes are stored.")}
    ${copyRow("file", active?.memory_path || "", "The memory file this chat loads; copy copies its path.")}
    <pre class="memory-content">${html(value || "No notes for this folder.")}</pre>`;
}

export function renderGeneralPage(page, active, pageContext) {
  useSettingsContext(pageContext);
  return ({ sessions, tools, memory })[page]?.(active) || "";
}
