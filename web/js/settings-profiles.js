let html, attr, store, errors, subhead, expanded;

function profileAgentID(agent) {
  const name = String(agent?.name || agent || "").trim().toLowerCase();
  return name === "agent_b" ? name : name.replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

export function renderProfilesPage(context) {
  ({ html, attr, store, errors, subhead, expanded = new Set() } = context);
  const activeSession = store.sessions?.[store.active];
  const notes = String(activeSession?.agent_memory_content || "").split(/\r?\n/).map((line) => line.trim()).filter((line) => line.startsWith("- ")).map((line) => line.slice(2));
  const memoryRows = notes.map((raw, index) => {
    const date = /^\d{4}-\d{2}-\d{2}/.exec(raw)?.[0] || "—";
    const tail = /\[([^\]]+)\]\s*$/.exec(raw)?.[1] || "";
    const kind = /scope:\s*([^,]+)/.exec(tail)?.[1]?.trim() || "note";
    const note = raw.replace(/^\d{4}-\d{2}-\d{2}\s*/, "").replace(/\s*\[[^\]]+\]\s*$/, "");
    const open = expanded.has(`profile-memory-note:${index}`);
    return `<tr class="profile-memory-note ${open ? "open" : ""}"><td>${html(date)}</td><td>${html(kind)}</td><td><button type="button" class="memory-note-text" data-action="profile-memory-note-toggle" data-id="${index}" title="${attr(note)}">${html(note)}</button></td><td><button type="button" data-action="remove-agent-memory" data-id="${attr(raw)}" data-confirm="this memory note">Remove</button></td></tr>`;
  }).join("");
  const memoryOpen = expanded.has("profile-memory");
  const memory = `<div class="profile-memory-block"><div class="setting-row profile-memory-row" title="Agent memory notes."><label>memory</label><div><span>${notes.length}</span><button type="button" data-action="profile-memory-toggle" aria-expanded="${memoryOpen}">${memoryOpen ? "Close" : "Open"}</button></div></div><table class="profile-memory-table" ${memoryOpen ? "" : "hidden"}><thead><tr><th>Date</th><th>Kind</th><th>Note</th><th>Remove</th></tr></thead><tbody>${memoryRows}</tbody></table></div>`;
  const agentID = activeSession?.agent_id || profileAgentID(store.config?.agents?.[0]);
  const agent = (store.config?.agents || []).find((item) => profileAgentID(item) === agentID) || {};
  const enabled = agent.skills || {};
  const badges = (store.skills || []).filter((skill) => skill.valid && (enabled[skill.name]?.enabled ?? skill.enabled)).map((skill) => `<span class="skill-badge">${html(skill.name)}</span>`).join("");
  const skills = `<div class="setting-row profile-skills-row" title="Skills enabled for this agent profile."><label>skills</label><div><span class="skill-badges">${badges || "none"}</span><button type="button" data-action="settings-section" data-id="skills" data-skill-profile="${attr(agentID)}">change in Skills</button></div></div>`;
  return `${subhead("Agent profiles")}<div data-adopt="agents-panel"></div>${skills}${memory}`;
}
import { actionTable } from "./settings-action-table.js";

export const profileActions = actionTable([
  "profile-memory-toggle", "profile-memory-note-toggle", "create-profile",
  "switch-profile", "rename-profile", "remove-agent-memory",
]);
