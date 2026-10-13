let html, attr, store, expanded;

function agentID(agent) {
  const name = String(agent?.name || "").trim().toLowerCase();
  return name === "agent_b" ? name : name.replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

export function renderSkillsPage(context) {
  ({ html, attr, store, expanded = new Set() } = context);
  const agents = store.config?.agents || [];
  const selected = context.skillAgent || agentID(agents[0]);
  const agent = agents.find((item) => agentID(item) === selected) || agents[0] || {};
  const settings = agent.skills || {};
  const profile = `<div class="setting-row" title="Agent profile whose skills are shown."><label>profile</label><div><select data-skills-profile>${agents.map((item) => `<option value="${attr(agentID(item))}" ${agentID(item) === selected ? "selected" : ""}>${html(item.name)}</option>`).join("")}</select></div></div>`;
  const rows = (store.skills || []).map((skill) => {
    const open = expanded.has(`skill:${skill.name}`);
    const on = settings[skill.name]?.enabled ?? skill.enabled;
    const invalid = !skill.valid;
    return `<tr class="skill-table-row ${open ? "open" : ""} ${invalid ? "invalid" : ""}">
      <td><button type="button" class="skill-badge" data-action="skill-row-toggle" data-id="${attr(skill.name)}">${html(skill.name || "invalid")}</button></td>
      <td><span class="skill-description" title="${attr(skill.description || skill.reason || "invalid")}">${html(skill.description || skill.reason || "invalid")}</span>${open ? `<div class="skill-detail">${html(skill.source || "unknown")} · ${skill.index_tokens || 0} tokens · last read ${html(skill.last_read || "never")}${(skill.warnings || []).length ? ` · ${html(skill.warnings.join(" · "))}` : ""}</div>` : ""}</td>
      <td>${invalid ? "—" : `<button type="button" role="switch" aria-checked="${on}" class="switch ${on ? "on" : ""}" data-action="skill-toggle" data-id="${attr(skill.name)}" data-agent-id="${attr(selected)}" data-value="${on ? "false" : "true"}"></button>`}</td>
      <td>${skill.included ? "" : `<button type="button" data-action="skill-remove" data-id="${attr(skill.name)}" data-agent-id="${attr(selected)}" data-confirm="skill ${attr(skill.name)}">Remove</button>`}</td>
    </tr>`;
  }).join("");
  const preview = context.skillPreview;
  const previewItems = preview?.rows || [];
  const previewRows = previewItems.map((item) => `<tr class="${item.valid ? "" : "invalid"}"><td>${item.valid ? `<input type="checkbox" data-skill-preview-id="${attr(item.id)}" ${item.included !== false ? "checked" : ""} ${item.shipped ? "disabled" : ""}>` : "—"}</td><td><span class="skill-badge">${html(item.name)}</span></td><td>${html(item.description || item.reason || "invalid")}${item.files ? ` · ${item.files} files` : ""}${item.shipped ? " · included; cannot replace" : item.existing ? " · Replace" : ""}</td></tr>`).join("");
  const eligible = previewItems.filter((item) => item.valid && !item.shipped);
  const addLabel = eligible.length === 1 && eligible[0].existing ? "Replace" : "Add";
  const previewBlock = preview ? `<table class="skill-add-preview"><tbody>${previewRows}</tbody></table>${eligible.length ? `<button type="button" data-action="skill-add-confirm">${addLabel}</button>` : ""}` : "";
  return `${profile}<table class="skills-table"><thead><tr><th>Skill</th><th>Description</th><th>On</th><th></th></tr></thead><tbody>${rows}</tbody></table><div class="setting-row" title="Choose a skill folder or a folder containing skills."><label>add</label><div><button type="button" data-action="skill-add-pick">Add skill</button></div></div>${previewBlock}`;
}
