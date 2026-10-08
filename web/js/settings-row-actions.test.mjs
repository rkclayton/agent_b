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
test("each connection row carries pencil Edit, save and delete at its right, and no input", () => {
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
	for (const action of ["connection-toggle", "save-connection", "remove-connection"]) {
    assert.match(row, new RegExp(`data-action="${action}"`), `${action} is not on the connection's own line`);
  }
	// Item 2qn: Test and Duplicate have left the header for the form, and Edit is a pencil.
  assert.doesNotMatch(row, /data-action="probe"/, "Test is still in the header");
	assert.doesNotMatch(row, /data-action="duplicate-connection"/, "Duplicate is still in the header");
	assert.doesNotMatch(row, />Edit<\/button>/);
	for (const icon of ["connectionIcons.edit", "connectionIcons.save", "connectionIcons.trash"]) {
    assert.ok(row.includes(icon), `${icon} is not used in the row`);
  }
  // (g): the three icon-only actions each carry a label.
  assert.equal((row.match(/aria-label=/g) || []).length, 3, "not every icon action is labelled");
  // Item 2px CHECK 1: the header holds no input element.
  const header = source.slice(source.indexOf('<div class="connection-row'), source.indexOf("\n      </div>`;", start));
  assert.doesNotMatch(header, /<input|<select|<textarea/, "the header holds an input");
});

// 2qw moves Duplicate beside Save so the primary line contains only its three actions.
test("Duplicate stays in the editor and Remove stays out of it", () => {
  const editor = sources["settings-connections.js"].slice(sources["settings-connections.js"].indexOf('<div class="settings-actions">'));
  const editorActions = editor.slice(0, editor.indexOf("</div>"));
	assert.match(editor, /data-action="save-connection"[^>]*>[\s\S]*data-action="duplicate-connection"/, "Duplicate is not beside Save");
	assert.doesNotMatch(editor, /data-action="remove-connection"/, "Remove moved into the editor");
	assert.match(editor, /data-action="measure-connection"/, "Eval lost its home in the editor");
});

test("Test Eval and Recommended are the three adjacent connection actions", () => {
  const source = sources["settings-connections.js"];
  const start = source.indexOf('<div class="connection-primary-actions settings-actions">');
  const actions = source.slice(start, source.indexOf("</div>", start));
  assert.ok(start >= 0, "the primary action line is missing");
  assert.match(actions, /"Test"[^]*"Eval"[^]*>Recommended</);
  assert.equal((actions.match(/<button/g) || []).length, 3);
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
	assert.match(settings, /registerMenu\(popover, \{ anchor: control, onClose: \(\) => \{ cancelConfirmation\(false\); popover\.remove\(\); \} \}\)/, "a press outside removes the popover without rebuilding the pressed control before its click");
	assert.match(settings, /function cancelConfirmation\(redraw = true\) \{[\s\S]{0,160}armed\.clear\(\);/, "cancel does not leave things as they were");
});
