import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const names = ["settings.js", "settings-connections.js", "settings-general.js", "settings-profiles.js", "settings-security.js", "settings-workspace.js"];
// Windows checks these files out with CRLF, so a boundary written with \n is not
// found there and a slice reads far more of the file than it means to.
const sources = Object.fromEntries(await Promise.all(names.map(async (name) =>
  [name, (await readFile(new URL(`./${name}`, import.meta.url), "utf8")).replace(/\r\n/g, "\n")])));
const all = Object.values(sources).join("\n");

// Item 2l5 (a) and (g). The operator: "make these icons: save (floppy disk) Test
// (no icon, small caps lettering button robotic) delete (trash can) duplicate (two
// robot heads layered on each other) but all these icons in the line on the right
// side of [the connection]".
test("each connection row carries save, Test, duplicate and delete at its right", () => {
  const row = sources["settings-connections.js"].slice(
    sources["settings-connections.js"].indexOf('<span class="connection-actions">'),
    sources["settings-connections.js"].indexOf("</span>\n      </div>"),
  );
  assert.ok(row, "the connection row has no actions span");
  for (const action of ["save-connection", "probe", "duplicate-connection", "remove-connection"]) {
    assert.match(row, new RegExp(`data-action="${action}"`), `${action} is not on the connection's own line`);
  }
  // Test is lettering, not an icon; the other three are icons.
  assert.match(row, /class="row-action test"[^>]*>\$\{connection\._probing \? "testing" : "test"\}/);
  for (const icon of ["connectionIcons.save", "connectionIcons.duplicate", "connectionIcons.trash"]) {
    assert.ok(row.includes(icon), `${icon} is not used in the row`);
  }
  // (g): three of four are icon-only, so every one carries a label.
  assert.equal((row.match(/aria-label=/g) || []).length, 4, "not every row action is labelled");
});

// (c): the controls the row replaces are gone from the editor, and the Evaluation
// Harness keeps its home there because it is not a per-row action.
test("no connection action is duplicated between the row and the editor", () => {
  const editor = sources["settings-connections.js"].slice(sources["settings-connections.js"].indexOf('<div class="settings-actions">'));
  const editorActions = editor.slice(0, editor.indexOf("</div>"));
  for (const action of ["save-connection", "duplicate-connection", "remove-connection"]) {
    assert.doesNotMatch(editorActions, new RegExp(`data-action="${action}"`), `${action} is in both the row and the editor`);
  }
  assert.match(editorActions, /data-action="measure-connection"/, "the Evaluation Harness lost its home in the editor");
  assert.doesNotMatch(editorActions, /data-action="probe"/, "Test is in both the row and the editor");
});

// (d): the row's save commits that connection and nothing else.
test("a connection's save commits only that connection", () => {
  assert.match(sources["settings.js"], /if \(action === "save-connection"\) return void saveConnection\(id\);/);
  assert.match(sources["settings.js"], /async function saveConnection\(id\) \{\s*const prefix = `connections\.\$\{id\}\.`;/);
  assert.match(sources["settings.js"], /if \(await saveSettings\(prefix\)\)/, "the row save does not scope its write to that connection");
});

// Item 2l4 (a) and (d). Not one remove control rewrites its own label any more, and
// every one of the seven that shared the pattern names what it will remove.
test("no remove control changes its label when armed", () => {
  for (const forbidden of [/Confirm ×/, /Confirm remove/, /Confirm revoke/, /Confirm clear/, /Confirm empty/, /armed\.has\(key\) \? "confirm" : ""/]) {
    assert.doesNotMatch(all, forbidden, `a control still rewrites itself: ${forbidden}`);
  }
  const confirmables = [...all.matchAll(/data-action="(remove-connection|close-session|remove-agent-memory|revoke-standing-grant|reset-session|revoke-workspace-policy|empty-operator-attachments)"[^>]*data-confirm=/g)];
  assert.equal(confirmables.length, 7, `expected all seven armed controls to name what they remove, found ${confirmables.length}`);
});

// (b) and (c): one anchored popover, Escape and outside click cancel, and cancel
// leaves everything as it was.
test("confirmation is one small anchored popover that cancels cleanly", () => {
  const settings = sources["settings.js"];
  assert.match(settings, /function confirmPopover\(\) \{/);
  assert.match(settings, /class="confirm-popover" role="dialog" aria-modal="false"/, "the popover takes over the view");
  assert.match(settings, /<p>Remove \$\{html\(question\)\}\?<\/p>/, "the popover does not name what will be removed");
  assert.match(settings, /data-action="confirm-cancel"/);
  assert.match(settings, /data-action="confirm-proceed"/);
  assert.match(settings, /if \(event\.key === "Escape" && open && confirmPending\) \{ cancelConfirmation\(\); return; \}/);
  assert.match(settings, /sheet\.addEventListener\("pointerdown"/, "a click outside does not cancel");
  assert.match(settings, /function cancelConfirmation\(\) \{[\s\S]{0,160}armed\.clear\(\);/, "cancel does not leave things as they were");
});
