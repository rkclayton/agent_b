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
  // The slice ends at the actions span's OWN close rather than at the row's closing
  // div: item 2nc (a) puts a refusal line between the two, and an end marker that
  // assumed they were adjacent silently swallowed the editor's markup with it.
  const source = sources["settings-connections.js"];
  const start = source.indexOf('<span class="connection-actions">');
  // And the end marker is the actions span's OWN close on its own line: item 2nn (c)
  // puts a span inside the Test control, so the first "</span>" after the start is
  // now that one and a slice ending there stops before three of the four controls.
  const row = source.slice(start, source.indexOf("\n          </span>", start));
  assert.ok(row, "the connection row has no actions span");
  for (const action of ["save-connection", "probe", "duplicate-connection", "remove-connection"]) {
    assert.match(row, new RegExp(`data-action="${action}"`), `${action} is not on the connection's own line`);
  }
  // Test is lettering, not an icon; the other three are icons. Item 2nn (c): while
  // the test runs the lettering gives way to the waiting element — "there are still
  // no loading bars. i hit test and it just says testing" — so the word the row
  // shows when idle is still "test" and the running state is a bar.
  assert.match(row, /class="row-action test"[^>]*>\$\{connection\._probing \? `<span class="probe-wait"/);
  assert.doesNotMatch(row, /"testing"/, "the row still shows the bare word while testing");
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
  // Item 2nn (d) supersedes half of this: the sheet reads top to bottom as the flow
  // — label, address, key, TEST, model, Evaluate, SAVE — so Test and Save are in the
  // editor by name, where the operator is when he needs them. The row keeps its copies
  // as the collapsed row's affordances. Duplicate and Remove are still row-only: they
  // act on the connection as a whole and have no place in its flow.
  for (const action of ["duplicate-connection", "remove-connection"]) {
    assert.doesNotMatch(editor, new RegExp(`data-action="${action}"[^>]*>(?![\s\S]*connection-actions)`), `${action} is in both the row and the editor`);
  }
  assert.match(editor, /data-action="measure-connection"/, "the Evaluation Harness lost its home in the editor");
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
