// Item 2gg (v1.1.2/W4): copying the transcript yields plain text, in order,
// with each entry's kind marked minimally and no UI chrome.
//
// The audit found select-all/copy produced bare text in which a question, an
// answer, a tool result and a harness line were indistinguishable — the record
// lost the one thing that makes it readable away from the page. Nothing is
// added to the DOM to fix it: the marks are produced when the copy happens,
// from the rows the selection covers, so the page itself is unchanged and the
// browser's own find still sees exactly the text it saw before.

// The marks. "Minimally" is the item's word: a speaker gets a name, everything
// else gets a rule, and nothing gets a box.
export const KIND_MARKS = [
  ["chat-user", "you:"],
  ["chat-summary", "— summary —"],
  ["tool-entry", "— tool —"],
  ["chat-response-step", "— tool —"],
  ["chat-notice-row", "— harness —"],
  ["chat-agent", "agent_b:"],
];

// markFor is the first mark whose class the row carries. Order matters: a row
// is `chat-entry chat-agent chat-response`, and a tool entry also carries
// chat-entry, so the specific classes are tested before the general one.
export function markFor(className) {
  const classes = String(className || "").split(/\s+/).filter(Boolean);
  for (const [name, mark] of KIND_MARKS) if (classes.includes(name)) return mark;
  return "";
}

// transcriptText turns rows into the copied text. A row is {className, text}.
// Blank rows are dropped; a row whose text already begins with its mark is not
// marked twice; entries are separated by one blank line, which is what makes a
// pasted transcript readable.
export function transcriptText(rows) {
  const blocks = [];
  for (const row of rows || []) {
    const text = String(row?.text ?? "").replace(/\r/g, "").replace(/[ \t]+$/gm, "").trim();
    if (!text) continue;
    const mark = markFor(row?.className);
    if (!mark) { blocks.push(text); continue; }
    if (text.startsWith(mark)) { blocks.push(text); continue; }
    blocks.push(mark.endsWith(":") ? `${mark} ${text}` : `${mark}\n${text}`);
  }
  return blocks.join("\n\n");
}

// rowsInSelection is every transcript entry the selection touches, in document
// order. Selecting part of one entry copies that entry.
export function rowsInSelection(log, selection) {
  if (!log || !selection || selection.rangeCount === 0) return [];
  const range = selection.getRangeAt(0);
  if (range.collapsed) return [];
  const rows = [...log.querySelectorAll(".chat-entry")];
  const touched = rows.filter((row) => range.intersectsNode(row));
  // A selection inside a single entry intersects only that entry; one that
  // covers the log intersects them all.
  return touched.map((row) => ({ className: row.className, text: row.innerText || "" }));
}

// installTranscriptCopy makes the copy event produce that text. It leaves the
// event alone when the selection is not in the transcript, so copying from the
// composer or anywhere else behaves exactly as before.
export function installTranscriptCopy(log, target = document) {
  if (!log || !target?.addEventListener) return;
  target.addEventListener("copy", (event) => {
    const selection = target.getSelection ? target.getSelection() : window.getSelection();
    const rows = rowsInSelection(log, selection);
    if (rows.length === 0) return;
    const text = transcriptText(rows);
    if (!text) return;
    // Only take the event over once the text is actually on the clipboard. An
    // event with no clipboardData would otherwise be prevented with nothing
    // written, and the operator's copy would come back empty (cold review).
    if (!event.clipboardData) return;
    event.clipboardData.setData("text/plain", text);
    event.preventDefault();
  });
}
