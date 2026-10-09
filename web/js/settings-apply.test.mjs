import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

// Windows checks these files out with CRLF, so a boundary written with \n is not
// found there and a slice reads far more of the file than it means to.
const settings = readFileSync(new URL("./settings.js", import.meta.url), "utf8").replace(/\r\n/g, "\n");

// Item 2l6 (d) keeps Save local to its setting; item 2sy restores one X as the
// window's close control and adds nothing else to the header.
test("the settings sheet has no sheet-wide Save and has only its close x", () => {
  assert.doesNotMatch(settings, /data-action="save-settings"[^>]*>/, "a sheet-wide Save control is still rendered");
  assert.doesNotMatch(settings, /class="settings-save"/, "the sheet-wide save styling is still applied to a control");
  assert.match(settings, /<div class="settings-head-actions"><button type="button" data-action="close" aria-label="Close" title="Close">×<\/button><\/div>/);
});

// (a) and (c). The four paths that need a complete value before they mean
// anything, and nothing else. Connection values commit on blur or selection.
test("only settings that need a complete value keep an explicit save", () => {
  const declaration = /const explicitSavePaths = new Set\(\[([^\]]*)\]\)/.exec(settings);
  assert.ok(declaration, "the explicit-save list is not declared");
  const paths = declaration[1].split(",").map((value) => value.trim().replace(/^"|"$/g, "")).filter(Boolean);
  assert.deepEqual(paths.sort(), ["memory.dir", "shell.deny", "tools.list_dir.ignore", "tools.shell.operator_commands"]);
  assert.match(settings, /function needsExplicitSave\(path\) \{\s*return explicitSavePaths\.has\(path\);/);
  assert.doesNotMatch(settings, /function needsExplicitSave\(path\) \{[^}]*connections\./);
});

// (b). A commit is blur, a toggle or a selection — never a keystroke.
test("a committed setting applies immediately and a keystroke does not", () => {
  assert.match(settings, /async function applySetting\(path\) \{/);
  // blur applies unless the path waits for an explicit save; connection fields
  // defer one event turn so an adjacent action receives its click before redraw.
  assert.match(settings, /if \(needsExplicitSave\(path\)\) \{[\s\S]{0,350}\} else if \(path\.startsWith\("connections\."\)\) \{[\s\S]{0,350}setTimeout\(\(\) => void applySetting\(path\), 0\);[\s\S]{0,60}\} else await applySetting\(path\);/, "blur does not apply an instant setting");
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
  assert.match(settings, /problem \? errorMarkup\(problem, `field:\$\{path\}`\) : note/, "a field error has nowhere to render");
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

test("a typed reserve the window cannot hold is lowered and the field says so (2px CHECK 8)", () => {
  assert.match(settings, /if \(saved && typedReserve > saved\.reserve_output\) fieldNotes\.set\(reservePath, `lowered to \$\{saved\.reserve_output\}, the most a \$\{saved\.n_ctx\}-token context allows`\)/);
  assert.match(settings, /errorMarkup\(problem, `field:\$\{path\}`\) : note/, "the note has nowhere to render");
});
