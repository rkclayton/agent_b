let html, attr, store, errors, subhead;

export function renderProfilesPage(context) {
  ({ html, attr, store, errors, subhead } = context);
  const state = store.profiles || store.config.profiles || { active: "", names: [] };
  const rows = (state.names || []).map((name) => {
    const active = name === state.active;
    return `<div class="setting-row profile-row" title="Switch or rename this operator profile.">
      <label>${html(name)}${active ? " · active" : ""}</label>
      <div class="settings-actions">
        <button type="button" data-action="switch-profile" data-id="${attr(name)}" ${active ? "disabled" : ""}>Switch</button>
        <input id="rename-profile-${attr(name)}" value="${attr(name)}" aria-label="Rename ${attr(name)}">
        <button type="button" data-action="rename-profile" data-id="${attr(name)}">Rename</button>
      </div>
    </div>`;
  }).join("");
  const activeSession = store.sessions?.[store.active];
  const notes = String(activeSession?.agent_memory_content || "").split(/\r?\n/).map((line) => line.trim()).filter((line) => line.startsWith("- ")).map((line) => line.slice(2));
  const memory = notes.length ? notes.map((note) => {
    const key = `agent-memory:${note}`;
    return `<div class="setting-row"><label>${html(note)}</label><div><button type="button" data-action="remove-agent-memory" data-id="${attr(note)}" data-confirm="this memory note">Remove</button></div></div>`;
  }).join("") : '<p class="settings-note inline">No agent-layer memory entries.</p>';
  const skills = (store.skills || []).map((skill) => `<div class="setting-row" title="${attr((skill.warnings || []).join(" · ") || skill.reason || "Profile skill")}">
    <label><strong>${html(skill.name || "invalid")}</strong><span class="settings-note inline">${html(skill.description || skill.reason || "invalid")}</span></label>
    <div class="settings-actions"><span>${html(skill.source)} · ${skill.index_tokens || 0} tokens · last read ${html(skill.last_read || "never")}${skill.valid ? "" : ` · invalid: ${html(skill.reason)}`}</span><button type="button" role="switch" aria-checked="${!!skill.enabled}" class="switch ${skill.enabled ? "on" : ""}" data-action="skill-toggle" data-id="${attr(skill.name)}" data-value="${skill.enabled ? "false" : "true"}" ${skill.valid ? "" : "disabled"}></button></div>
  </div>`).join("");
  return `${errors.get("profiles") ? `<p class="field-error">${html(errors.get("profiles"))}</p>` : ""}${rows || '<p class="settings-note inline">No profiles configured.</p>'}
    ${subhead("Create profile", "Creates an empty operator profile; connections remain shared.")}
    <div class="setting-row" title="Create an empty operator profile; connections stay shared."><label for="new-profile-name">name</label><div class="settings-actions"><input id="new-profile-name" maxlength="64"><button type="button" data-action="create-profile">Create</button></div></div>
    ${subhead("Skills", "Skills are on when added; use the switch to turn one off. Their scripts use the ordinary approval policy.")}
    ${skills || '<p class="settings-note inline">No skills found.</p>'}
    <div class="setting-row" title="Import one skill folder or ZIP archive into this profile."><label for="skill-import-path">folder to import</label><div class="settings-actions"><input id="skill-import-path" placeholder="C:\\path\\to\\skill"><button type="button" data-action="skill-import">Import</button><button type="button" data-action="skill-rescan">Rescan</button></div></div>
    ${subhead("Agent memory", "Durable notes loaded by every chat of this agent. Remove deletes only the named entry.")}${memory}`;
}
