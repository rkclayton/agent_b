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
  return `${errors.get("profiles") ? `<p class="field-error">${html(errors.get("profiles"))}</p>` : ""}${rows || '<p class="settings-note inline">No profiles configured.</p>'}
    ${subhead("Create profile", "Creates an empty operator profile; connections remain shared.")}
    <div class="setting-row" title="Create an empty operator profile; connections stay shared."><label for="new-profile-name">name</label><div class="settings-actions"><input id="new-profile-name" maxlength="64"><button type="button" data-action="create-profile">Create</button></div></div>
    ${subhead("Agent memory", "Durable notes loaded by every chat of this agent. Remove deletes only the named entry.")}${memory}`;
}
