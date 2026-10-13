import assert from "node:assert/strict";
import test from "node:test";

import { renderProfilesPage } from "./settings-profiles.js";
import { renderSkillsPage } from "./settings-skills.js";

const helpers = {
  html: String,
  attr: String,
  subhead: (label) => `<h2>${label}</h2>`,
  expanded: new Set(),
};

const store = {
  active: "chat",
  sessions: { chat: { agent_id: "agent_b", agent_memory_content: "" } },
  config: {
    agents: [
      { name: "agent_b", skills: { shipped: { enabled: true }, added: { enabled: false } } },
      { name: "Review", skills: { shipped: { enabled: false }, added: { enabled: true } } },
    ],
  },
  skills: [
    { name: "shipped", description: "Built in", valid: true, included: true, enabled: true },
    { name: "added", description: "User skill", valid: true, included: false, enabled: false },
  ],
};

test("Skills is profile-scoped and protects shipped skills 2u7", () => {
  const page = renderSkillsPage({ ...helpers, store, skillAgent: "agent_b" });
  assert.match(page, /<th>Skill<\/th><th>Description<\/th><th>On<\/th>/);
  assert.match(page, /option value="agent_b" selected/);
  assert.match(page, /data-id="shipped"[^>]*aria-checked="true"|aria-checked="true"[^>]*data-id="shipped"/);
  assert.doesNotMatch(page.match(/<tr[^>]*>[\s\S]*?shipped[\s\S]*?<\/tr>/)?.[0] || "", />Remove</);
  assert.match(page.match(/<tr[^>]*>[\s\S]*?added[\s\S]*?<\/tr>/)?.[0] || "", />Remove</);
  assert.match(page, /data-action="skill-add-pick">Add skill/);
  assert.doesNotMatch(page, /Hermes/i);

  const review = renderSkillsPage({ ...helpers, store, skillAgent: "review" });
  assert.match(review, /option value="review" selected/);
  assert.match(review, /data-id="added"[^>]*aria-checked="true"|aria-checked="true"[^>]*data-id="added"/);
});

test("Profiles shows badges and sends changes to Skills without skill controls 2u7", () => {
  const page = renderProfilesPage({ ...helpers, store, errors: new Map() });
  assert.match(page, /skill-badge">shipped/);
  assert.match(page, /data-id="skills" data-skill-profile="agent_b">change in Skills/);
  assert.doesNotMatch(page, /skill-toggle|skill-add-pick|Hermes/i);
});

test("Add preview names replacement and disables included replacement 2u7", () => {
  const page = renderSkillsPage({
    ...helpers,
    store,
    skillAgent: "agent_b",
    skillPreview: { rows: [
      { id: "skill:added", name: "added", valid: true, included: true, existing: true, files: 3 },
      { id: "skill:shipped", name: "shipped", valid: true, included: true, shipped: true },
      { id: "invalid", name: "broken", valid: false, reason: "SKILL.md is missing" },
    ] },
  });
  assert.match(page, /added[\s\S]*3 files · Replace/);
  assert.match(page, /skill:shipped" checked disabled/);
  assert.match(page, /included; cannot replace/);
  assert.match(page, /broken[\s\S]*SKILL.md is missing/);
});

test("A lone stored skill offers Replace and an invalid folder offers no action 2u7", () => {
  const replacement = renderSkillsPage({ ...helpers, store, skillAgent: "agent_b", skillPreview: { rows: [
    { id: "skill:added", name: "added", description: "New copy", valid: true, included: true, existing: true },
  ] } });
  assert.match(replacement, /data-action="skill-add-confirm">Replace</);
  const invalid = renderSkillsPage({ ...helpers, store, skillAgent: "agent_b", skillPreview: { rows: [
    { id: "invalid", name: "broken", valid: false, reason: "SKILL.md is missing" },
  ] } });
  assert.doesNotMatch(invalid, /data-action="skill-add-confirm"/);
});

test("Long descriptions stay in the row and invalid skills have no switch 2u7", () => {
  const description = "x".repeat(300);
  const varied = { ...store, skills: [
    ...store.skills,
    { name: "long", description, valid: true, included: false },
    { name: "broken", reason: "frontmatter name is missing", valid: false, included: false },
  ] };
  const closed = renderSkillsPage({ ...helpers, store: varied, skillAgent: "agent_b" });
  assert.match(closed, new RegExp(description));
  assert.match(closed, /skill-table-row\s+invalid[\s\S]*?broken[\s\S]*?frontmatter name is missing[\s\S]*?<td>—<\/td>/);
  const opened = renderSkillsPage({ ...helpers, store: varied, skillAgent: "agent_b", expanded: new Set(["skill:long"]) });
  assert.match(opened, /skill-table-row open/);
});
