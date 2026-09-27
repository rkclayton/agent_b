// Item 17-i: the reflection section, now on Settings under Activity (2gk).
// Read-only text - the latest overview and the latest tool-candidate report -
// fetched when that section opens and again after a run closes. It adds no
// control.

const stamp = (value) => {
  if (!value) return "";
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? "" : `${at.toISOString().replace("T", " ").slice(0, 16)} UTC`;
};

// reflectionLabel is the caption beside the section's title.
export function reflectionLabel(answer = {}) {
  if (!answer.enabled) return "off on this install";
  const parts = [];
  if (answer.at) parts.push(`overview ${stamp(answer.at)}`);
  if (answer.tier) parts.push(answer.tier);
  if (answer.report_at) parts.push(`report ${stamp(answer.report_at)}`);
  return parts.join(" · ") || "after each run, and daily";
}

// reflectionText is what the two panes show for an answer.
export function reflectionText(answer = {}) {
  if (!answer.enabled) return { overview: "Reflection is off on this install.", report: "" };
  return { overview: answer.overview || "No reflection pass has run yet.", report: answer.report || "" };
}

export async function loadReflection(fetcher = fetch, doc = globalThis.document) {
  const overview = doc?.getElementById("panel-reflection-overview");
  if (!overview) return null;
  let answer;
  try {
    const response = await fetcher("/api/reflection");
    if (!response.ok) throw new Error(String(response.status));
    answer = await response.json();
  } catch {
    return null;
  }
  const text = reflectionText(answer);
  overview.textContent = text.overview;
  const report = doc.getElementById("panel-reflection-report");
  if (report) report.textContent = text.report;
  const when = doc.getElementById("panel-reflection-when");
  if (when) when.textContent = reflectionLabel(answer);
  return answer;
}

// Item 2mw (a): ONE ROW PER NOTE REFLECTION WROTE, with the controls that make it the
// operator's. Reflection used to be read-only text, so a correction it found stayed
// "unconfirmed" forever and carried that word into every prompt that loaded it.
//
// A row shows the note, the day it was written, which layer holds it and which run
// produced it - (b)'s provenance, read from the note line itself rather than from a
// second store. Confirm and Delete act at once; Edit opens the words for correcting
// and confirms them together. Restore brings back anything deleted inside the stated
// period, which the caption names.

const escape = (value) =>
  String(value ?? "").replace(/[&<>"']/g, (one) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[one]);

// noteState is the word a row shows for where a note stands.
export function noteState(note = {}) {
  const state = String(note.reflection || "");
  if (state.startsWith("confirmed")) {
    const [, when] = state.split(" ");
    return when ? `confirmed ${when}` : "confirmed";
  }
  return state || "unconfirmed";
}

// noteProvenance is the quiet line under a note: the day, the layer, the run.
export function noteProvenance(note = {}) {
  const parts = [];
  if (note.date) parts.push(note.date);
  if (note.layer) parts.push(note.layer);
  if (note.run) parts.push(`run ${note.run}`);
  return parts.join(" · ");
}

// notesCaption states (c)'s period and (f)'s cap, where the operator is looking at the
// notes rather than only in the reference documentation.
export function notesCaption(answer = {}) {
  const parts = [];
  const rows = (answer.notes || []).length;
  parts.push(rows === 1 ? "1 note" : `${rows} notes`);
  if (answer.total > rows) parts.push(`of ${answer.total} in memory`);
  if ((answer.removed || []).length) parts.push(`${answer.removed.length} deleted, restorable for ${answer.retention_days} days`);
  if (answer.max_tokens) parts.push(`each layer loads up to ${answer.max_tokens} tokens`);
  return parts.join(" · ");
}

// rowsMarkup builds the list. An empty answer is a sentence, not an empty box.
export function rowsMarkup(answer = {}) {
  const notes = answer.notes || [];
  const removed = answer.removed || [];
  if (!notes.length && !removed.length) {
    return `<p class="reflection-note-empty">Reflection has written no notes yet. When it does, each one appears here to confirm, correct or delete.</p>`;
  }
  const row = (note, kind) => `
    <div class="reflection-note" data-file="${escape(note.file)}" data-note="${escape(note.text)}">
      <div class="reflection-note-text">${escape(note.text)}</div>
      <div class="reflection-note-meta"><span>${escape(noteProvenance(note))}</span><span class="reflection-note-state">${escape(kind === "removed" ? `deleted ${note.date || ""}` : noteState(note))}</span></div>
      <div class="reflection-note-actions">${
        kind === "removed"
          ? `<button type="button" data-note-action="restore">Restore</button>`
          : `${noteState(note).startsWith("confirmed") ? "" : `<button type="button" data-note-action="confirm">Confirm</button>`}<button type="button" data-note-action="edit">Edit</button><button type="button" data-note-action="delete">Delete</button>`
      }</div>
    </div>`;
  return notes.map((note) => row(note, "note")).join("") + removed.map((note) => row(note, "removed")).join("");
}

// loadReflectionNotes fills the rows. It is called with loadReflection, so the list
// refreshes on the same events the overview does.
export async function loadReflectionNotes(fetcher = fetch, doc = globalThis.document) {
  const host = doc?.getElementById("panel-reflection-notes");
  if (!host) return null;
  let answer;
  try {
    const response = await fetcher("/api/reflection-notes");
    if (!response.ok) throw new Error(String(response.status));
    answer = await response.json();
  } catch {
    return null;
  }
  host.innerHTML = rowsMarkup(answer);
  const caption = doc.getElementById("panel-reflection-notes-when");
  if (caption) caption.textContent = notesCaption(answer);
  return answer;
}

// Item 2mw (a) and (c): the four controls. Confirm and Restore are one request each.
// Edit turns the row's text into a field and confirms the operator's words with it.
// Delete asks first, because it is the only one of the four that loses something -
// and even then it is restorable, which the confirmation says.
export async function reflectionNoteAction(action, row, poster = fetch) {
  const file = row?.dataset?.file || "";
  const note = row?.dataset?.note || "";
  if (!file || !note) return { changed: false };
  const send = async (path, body) => {
    const response = await poster(path, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
    if (!response.ok) throw new Error(String(response.status));
    return response.json();
  };
  if (action === "confirm") return send("/api/reflection-notes/confirm", { file, note });
  if (action === "restore") return send("/api/reflection-notes/restore", { file, note });
  if (action === "delete") return send("/api/reflection-notes/remove", { file, note, confirm: true });
  if (action === "edit") {
    const edit = row.dataset.edit || "";
    if (!edit.trim() || edit.trim() === note.trim()) return { changed: false };
    return send("/api/reflection-notes/confirm", { file, note, edit });
  }
  return { changed: false };
}

// wireReflectionNotes delegates from the list, so rows replaced by a reload need no
// re-binding. Edit is two presses: the first opens the words, the second saves them.
export function wireReflectionNotes(doc = globalThis.document, poster = fetch, reload = loadReflectionNotes) {
  const host = doc?.getElementById("panel-reflection-notes");
  if (!host || host.dataset.wired === "yes") return;
  host.dataset.wired = "yes";
  host.addEventListener("click", async (event) => {
    const button = event.target?.closest?.("[data-note-action]");
    if (!button) return;
    const row = button.closest(".reflection-note");
    const action = button.dataset.noteAction;
    if (action === "edit" && !row.dataset.editing) {
      // Open the words in place. The row keeps the original note in its dataset, so
      // the request can still name the note it is replacing.
      const field = doc.createElement("textarea");
      field.className = "reflection-note-field";
      field.value = row.dataset.note;
      row.querySelector(".reflection-note-text").replaceWith(field);
      row.dataset.editing = "yes";
      button.textContent = "Save";
      field.focus();
      return;
    }
    if (action === "edit") {
      row.dataset.edit = row.querySelector(".reflection-note-field")?.value || "";
    }
    if (action === "delete" && !row.dataset.armed) {
      // Asked first, and told that it is undoable.
      row.dataset.armed = "yes";
      button.textContent = "Delete, keep 30 days";
      return;
    }
    for (const one of row.querySelectorAll("[data-note-action]")) one.disabled = true;
    try {
      await reflectionNoteAction(action, row, poster);
    } finally {
      await reload(poster === fetch ? fetch : poster, doc);
    }
  });
}
