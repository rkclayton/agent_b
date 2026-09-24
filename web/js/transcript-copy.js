import { callServiceKey } from "./call-service-display.js";
import { formatDuration } from "./duration.js";
import { isHeaderlessSteps, responseBlocks, responseSummary, thoughtTokens } from "./chat-response-groups.js";

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

// Copy records are deliberately kept out of the DOM. This prevents transcript
// semantics from becoming hidden page text, browser-find noise, or UI
// furniture while still letting copy reproduce the durable record.
const copyRecords = new WeakMap();

export function setTranscriptCopyRecord(node, record) {
  if (!node || (typeof node !== "object" && typeof node !== "function")) return;
  if (typeof record === "string" && record.length) copyRecords.set(node, record);
  else copyRecords.delete(node);
}

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
    if (typeof row?.record === "string" && row.record.length) {
      blocks.push(row.record);
      continue;
    }
    const text = String(row?.text ?? "").replace(/\r/g, "").replace(/[ \t]+$/gm, "").trim();
    if (!text) continue;
    const mark = markFor(row?.className);
    if (!mark) { blocks.push(text); continue; }
    if (text.startsWith(mark)) { blocks.push(text); continue; }
    blocks.push(mark.endsWith(":") ? `${mark} ${text}` : `${mark}\n${text}`);
  }
  return blocks.join("\n\n");
}

export function responseTranscriptRecord(entry, expanded = new Set(), author = "agent_b", active = false) {
  const lines = [`${author}:`];
  for (const block of responseBlocks(entry?.items || [])) {
    if (block.steps.length) {
      const summary = responseSummary(block.steps);
      const parts = [];
      if (summary.tools) parts.push(`${summary.tools} tool ${summary.tools === 1 ? "call" : "calls"}`);
      if (summary.failed) parts.push(`${summary.failed} failed`);
      if (!summary.tools && summary.thoughts) parts.push(`${summary.thoughts} ${summary.thoughts === 1 ? "thought" : "thoughts"}`);
      if (summary.duration || summary.tools || summary.thoughts) parts.push(formatDuration(summary.duration || 0));
      lines.push(`steps${parts.length ? ` · ${parts.join(" · ")}` : ""}`);

      const blockOpen = active || isHeaderlessSteps(block.steps) || expanded.has(block.key);
      for (const item of block.steps) {
        if (item?.type === "agent") {
          const duration = formatDuration(item.thinkingMS || 0);
          lines.push(`thought ${duration} (~${thoughtTokens(item)} tokens)`);
          if (blockOpen && expanded.has(item.key) && item.reasoning) appendIndented(lines, item.reasoning);
        } else if (item?.type === "tool") {
          const argument = keyArgument(item.args);
          const state = item.result?.ok === false ? "error" : "ok";
          lines.push(`tool ${item.name || "tool"}${argument ? ` ${argument}` : ""} → ${state} ${formatDuration(item.result?.ms || 0)}`);
          if (blockOpen && expanded.has(item.key) && item.content) appendIndented(lines, item.content);
        } else if (item?.type === "notice") {
          lines.push(`— harness —${item.text ? `\n${item.text}` : ""}`);
        }
      }
    }
    if (block.prose?.text) lines.push(block.prose.text);
  }
  return lines.join("\n");
}

function appendIndented(lines, value) {
  lines.push(...String(value).replace(/\r/g, "").split("\n").map((line) => `  ${line}`));
}

function keyArgument(args) {
  try {
    if (!args || typeof args !== "object" || Array.isArray(args)) return "";
    const service = callServiceKey(args);
    if (service) return service;
    for (const key of ["path", "command", "pattern", "note"]) if (args[key] !== undefined) return String(args[key]);
    const first = Object.values(args)[0];
    return first === undefined ? "" : typeof first === "string" ? first : JSON.stringify(first);
  } catch {
    // Copy composition must not widen a malformed step into a failed turn.
    // The renderer owns the visible per-step failure and the rest still copies.
    return "";
  }
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
  return touched.map((row) => ({ className: row.className, text: row.innerText || "", record: copyRecords.get(row) }));
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
