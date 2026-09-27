import assert from "node:assert/strict";
import test from "node:test";

import {
  loadReflection,
  noteProvenance,
  noteState,
  notesCaption,
  reflectionLabel,
  reflectionNoteAction,
  reflectionText,
  rowsMarkup,
} from "./reflection.js";

const fakeDocument = () => {
  const elements = new Map([
    ["panel-reflection-overview", { textContent: "" }],
    ["panel-reflection-report", { textContent: "" }],
    ["panel-reflection-when", { textContent: "" }],
  ]);
  return { getElementById: (id) => elements.get(id) || null, elements };
};

test("the reflection caption names the pass, its tier and the report", () => {
  assert.equal(reflectionLabel({ enabled: true, at: "2026-09-20T01:02:03Z", tier: "tier 1 (Go)", report_at: "2026-09-20T02:00:00Z" }),
    "overview 2026-09-20 01:02 UTC · tier 1 (Go) · report 2026-09-20 02:00 UTC");
  assert.equal(reflectionLabel({ enabled: true }), "after each run, and daily");
  assert.equal(reflectionLabel({}), "off on this install");
});

test("an install with no pass yet says so rather than showing an empty pane", () => {
  assert.deepEqual(reflectionText({ enabled: true }), { overview: "No reflection pass has run yet.", report: "" });
  assert.equal(reflectionText({}).overview, "Reflection is off on this install.");
});

test("Console fills both panes from the endpoint and adds no control", async () => {
  const doc = fakeDocument();
  const answer = { enabled: true, at: "2026-09-20T01:02:03Z", tier: "tier 1 (Go: go list)", overview: "Reflection on p1", report_at: "2026-09-20T01:05:00Z", report: "Tool candidates" };
  const loaded = await loadReflection(async () => ({ ok: true, json: async () => answer }), doc);
  assert.deepEqual(loaded, answer);
  assert.equal(doc.elements.get("panel-reflection-overview").textContent, "Reflection on p1");
  assert.equal(doc.elements.get("panel-reflection-report").textContent, "Tool candidates");
  assert.match(doc.elements.get("panel-reflection-when").textContent, /tier 1/);
});

test("a failed fetch leaves the section as it was", async () => {
  const doc = fakeDocument();
  doc.elements.get("panel-reflection-overview").textContent = "previous";
  assert.equal(await loadReflection(async () => ({ ok: false, status: 500 }), doc), null);
  assert.equal(doc.elements.get("panel-reflection-overview").textContent, "previous");
  assert.equal(await loadReflection(async () => { throw new Error("offline"); }, doc), null);
});

// Item 2mw (a): the rows, and what each one says about where a note stands.
test("a row says whether a note is unconfirmed or when it was confirmed", () => {
  assert.equal(noteState({ reflection: "unconfirmed" }), "unconfirmed");
  assert.equal(noteState({ reflection: "confirmed 2026-09-27" }), "confirmed 2026-09-27");
  // A note with no marker at all is still shown as unconfirmed rather than blank.
  assert.equal(noteState({}), "unconfirmed");
});

// (b): the provenance a row shows comes from the note line, not from a second store.
test("a row names the day, the layer and the run that produced the note", () => {
  assert.equal(noteProvenance({ date: "2026-09-27", layer: "folder", run: "r12" }), "2026-09-27 · folder · run r12");
  assert.equal(noteProvenance({ date: "2026-09-27", layer: "machine" }), "2026-09-27 · machine");
  assert.equal(noteProvenance({}), "");
});

// (c) and (f): the caption states the retention period and the retrieval cap, where
// the operator is looking at the notes.
test("the notes caption states the restore period and the layer's token cap", () => {
  const caption = notesCaption({ notes: [{}, {}], total: 5, removed: [{}], retention_days: 30, max_tokens: 1500 });
  assert.match(caption, /2 notes/);
  assert.match(caption, /of 5 in memory/);
  assert.match(caption, /1 deleted, restorable for 30 days/);
  assert.match(caption, /each layer loads up to 1500 tokens/);
  assert.equal(notesCaption({ notes: [{}], retention_days: 30 }), "1 note");
});

test("an install with no notes yet says so rather than showing an empty list", () => {
  const markup = rowsMarkup({});
  assert.match(markup, /Reflection has written no notes yet/);
  assert.equal(markup.includes("data-note-action"), false);
});

test("an unconfirmed note offers confirm, edit and delete; a confirmed one does not offer confirm", () => {
  const unconfirmed = rowsMarkup({ notes: [{ text: "PowerShell 7 is the shell", date: "2026-09-27", run: "r1", layer: "folder", file: "repo-abcd1234.md", reflection: "unconfirmed" }] });
  for (const action of ["confirm", "edit", "delete"]) {
    assert.match(unconfirmed, new RegExp(`data-note-action="${action}"`));
  }
  // The row carries what an action needs: the layer FILE and the exact note.
  assert.match(unconfirmed, /data-file="repo-abcd1234\.md"/);
  assert.match(unconfirmed, /data-note="PowerShell 7 is the shell"/);

  const confirmed = rowsMarkup({ notes: [{ text: "x", file: "f.md", reflection: "confirmed 2026-09-27" }] });
  assert.equal(confirmed.includes('data-note-action="confirm"'), false);
  assert.match(confirmed, /data-note-action="delete"/);
});

test("a deleted note offers only restore, and says when it was deleted", () => {
  const markup = rowsMarkup({ notes: [], removed: [{ text: "gone", date: "2026-09-20", file: "f.md" }], retention_days: 30 });
  assert.match(markup, /data-note-action="restore"/);
  assert.equal(markup.includes('data-note-action="delete"'), false);
  assert.match(markup, /deleted 2026-09-20/);
});

// A note's words reach the page as text, never as markup.
test("a note that contains markup is escaped in the row and in its dataset", () => {
  const markup = rowsMarkup({ notes: [{ text: '<img src=x onerror="boom">', file: "f.md", reflection: "unconfirmed" }] });
  assert.equal(markup.includes("<img"), false);
  assert.match(markup, /&lt;img/);
  assert.equal(markup.includes('onerror="boom"'), false);
});

// (a) and (c): each control posts the note and its layer file, and nothing else.
test("each control posts the note it names, and delete asks for confirmation in the body", async () => {
  const sent = [];
  const poster = async (path, options) => {
    sent.push({ path, body: JSON.parse(options.body) });
    return { ok: true, json: async () => ({ changed: true }) };
  };
  const row = { dataset: { file: "f.md", note: "a belief" } };

  await reflectionNoteAction("confirm", row, poster);
  await reflectionNoteAction("restore", row, poster);
  await reflectionNoteAction("delete", row, poster);
  assert.deepEqual(sent.map((one) => one.path), [
    "/api/reflection-notes/confirm",
    "/api/reflection-notes/restore",
    "/api/reflection-notes/remove",
  ]);
  for (const one of sent) {
    assert.equal(one.body.file, "f.md");
    assert.equal(one.body.note, "a belief");
  }
  assert.equal(sent[2].body.confirm, true);

  // An edit sends the operator's words beside the note being replaced.
  sent.length = 0;
  await reflectionNoteAction("edit", { dataset: { file: "f.md", note: "a belief", edit: "a better belief" } }, poster);
  assert.equal(sent[0].path, "/api/reflection-notes/confirm");
  assert.equal(sent[0].body.edit, "a better belief");

  // An edit that changed nothing sends nothing.
  sent.length = 0;
  const unchanged = await reflectionNoteAction("edit", { dataset: { file: "f.md", note: "a belief", edit: " a belief " } }, poster);
  assert.equal(sent.length, 0);
  assert.equal(unchanged.changed, false);

  // A row missing either half sends nothing rather than posting a partial request.
  sent.length = 0;
  await reflectionNoteAction("confirm", { dataset: { file: "", note: "a belief" } }, poster);
  assert.equal(sent.length, 0);
});
