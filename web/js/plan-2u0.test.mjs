import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

test("Plan add preview has Folder Repository Plan and AGENTS before Add 2u0", () => {
  const html = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  const preview = html.match(/id="plan-add-preview"[\s\S]*?<\/div>\s*<p id="plan-add-error"/)?.[0] || "";
  for (const label of ["Folder", "Repository", "Plan", "AGENTS.md"]) assert.match(preview, new RegExp(`>${label}<`));
  assert.match(preview, /id="plan-add-confirm"[^>]*>Add</);
});

test("Plan presents five facts and Chats and Changes tables with no automatic continuation 2u0", () => {
  const html = fs.readFileSync(new URL("../index.html", import.meta.url), "utf8");
  const script = fs.readFileSync(new URL("plan.js", import.meta.url), "utf8");
  for (const label of ["Folder", "Repository", "Plan", "AGENTS.md", "Notes"]) assert.match(script, new RegExp(`\\["${label}"`));
  assert.match(html, />Chats<[\s\S]*id="plan-chats"/);
  assert.match(html, />Changes<[\s\S]*id="plan-changes"/);
  assert.doesNotMatch(html + script, /auto[_-]?continue/i);
  assert.match(script, /plan_id: selected, stop: !!goState\.running/);
});
