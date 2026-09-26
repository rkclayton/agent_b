import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const settings = readFileSync(new URL("./settings.js", import.meta.url), "utf8");

// Item 2l6 (d). The operator: "i want to remove the save button and the x in the
// upper right theres no need for it. to page away you click the chat tab or plan
// ect."
test("the settings sheet has no sheet-wide Save and no close x", () => {
  assert.doesNotMatch(settings, /data-action="save-settings"[^>]*>/, "a sheet-wide Save control is still rendered");
  assert.doesNotMatch(settings, /aria-label="Close settings"/, "the close x is still rendered");
  assert.doesNotMatch(settings, /class="settings-save"/, "the sheet-wide save styling is still applied to a control");
  assert.match(settings, /<div class="settings-head-actions"><\/div>/, "the header actions were not emptied");
});

// (a) and (c). The four paths that need a complete value before they mean
// anything, and nothing else. Connection values ride 2l5's per-connection save.
test("only settings that need a complete value keep an explicit save", () => {
  const declaration = /const explicitSavePaths = new Set\(\[([^\]]*)\]\)/.exec(settings);
  assert.ok(declaration, "the explicit-save list is not declared");
  const paths = declaration[1].split(",").map((value) => value.trim().replace(/^"|"$/g, "")).filter(Boolean);
  assert.deepEqual(paths.sort(), ["memory.dir", "shell.deny", "tools.list_dir.ignore", "tools.shell.operator_commands"]);
  assert.match(settings, /function needsExplicitSave\(path\) \{\s*return explicitSavePaths\.has\(path\) \|\| path\.startsWith\("connections\."\);/);
});

// (b). A commit is blur, a toggle or a selection — never a keystroke.
test("a committed setting applies immediately and a keystroke does not", () => {
  assert.match(settings, /async function applySetting\(path\) \{/);
  // blur applies unless the path waits for an explicit save
  assert.match(settings, /if \(needsExplicitSave\(path\)\) \{[\s\S]{0,200}\} else await applySetting\(path\);/, "blur does not apply an instant setting");
  // a toggle or choice is itself the commit
  assert.match(settings, /render\(\);\s*return void applySetting\(path\);/, "a toggle or selection does not apply");
  // the input (per-keystroke) listener never applies
  const inputListener = settings.slice(settings.indexOf('sheet.addEventListener("input"'), settings.indexOf("subscribe((_state, event)"));
  assert.doesNotMatch(inputListener, /applySetting/, "typing applies a setting per keystroke");
});

// (b). An invalid value does not apply and leaves the stored value alone: only the
// one committed path is ever sent, and the field-level error renders beside it.
test("an invalid value is reported beside its field and nothing else is sent", () => {
  assert.match(settings, /const ok = await saveSettings\(path\);/, "applySetting does not send exactly one path");
  assert.match(settings, /const entries = \[\.\.\.drafts\.entries\(\)\]\.filter\(\(\[path\]\) => !pathPrefix \|\| path\.startsWith\(pathPrefix\)\)/);
  assert.match(settings, /errors\.set\(error\.field \|\| "config", error\.message\)/, "a field error is not attached to its field");
  assert.match(settings, /<p class="field-error">/, "a field error has nowhere to render");
});

// (e). The guard narrows to the groups that still have an explicit save.
test("navigating away only asks about settings that still wait for a save", () => {
  assert.match(settings, /function pendingExplicitSaves\(\) \{\s*return \[\.\.\.drafts\.keys\(\)\]\.filter\(needsExplicitSave\);/);
  const leave = settings.slice(settings.indexOf("async function leaveSettingsForChat()"), settings.indexOf("function render()"));
  assert.match(leave, /pendingExplicitSaves\(\)\.length/, "the guard still fires for any draft");
  assert.doesNotMatch(leave, /if \(drafts\.size &&/, "the old any-draft guard survived");
});

// (b) and (c) in the row itself: it says it applied, or it carries its own save.
test("a row reports that it applied, or carries the save that belongs to it", () => {
  assert.match(settings, /const appliedSettings = new Map\(\);/);
  assert.match(settings, /<span class="setting-applied" aria-live="polite">applied<\/span>/);
  assert.match(settings, /data-action="save-setting" data-save-path="\$\{attr\(path\)\}"/);
  assert.match(settings, /if \(action === "save-setting"\) return void applySetting\(button\.dataset\.savePath\);/);
  // data-path stays the mark of a control whose VALUE is read and written, so a
  // selector for a field never also finds the save beside it.
  assert.doesNotMatch(settings, /data-action="save-setting" data-path=/);
});
