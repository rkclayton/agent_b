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
// Item 2lc (c): a mark is a change of speaker, not a row. A selection inside one
// entry gets no mark at all; one that spans speakers marks each speaker's portion
// once and separates them with a blank line.
export function transcriptText(rows) {
  const parts = [];
  for (const row of rows || []) {
    const text = typeof row?.record === "string" && row.record.length
      ? normalize(row.record)
      : normalize(row?.text);
    if (!text) continue;
    parts.push({ mark: markFor(row?.className), text, fromSelection: row?.fromSelection === true });
  }
  // Within one entry there is no change of speaker, so there is no mark.
  if (parts.length === 1 && parts[0].fromSelection) return parts[0].text;
  const blocks = [];
  let previous = null;
  for (const part of parts) {
    if (part.mark && part.mark !== previous && !part.text.startsWith(part.mark)) {
      blocks.push(part.mark.endsWith(":") ? `${part.mark} ${part.text}` : `${part.mark}\n${part.text}`);
    } else blocks.push(part.text);
    previous = part.mark;
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
          const delegated = item.name === "delegate" ? item.result?.delegate : null;
          const argument = delegated ? delegateKey(item.args) : keyArgument(item.args);
          const state = item.result?.ok === false ? "error" : "ok";
          lines.push(`tool ${item.name || "tool"}${argument ? ` ${argument}` : ""} → ${state} ${formatDuration(item.result?.ms || 0)}`);
          if (blockOpen && expanded.has(item.key)) {
            const detail = delegated ? delegateDetail(item.args, delegated) : item.content;
            if (detail) appendIndented(lines, detail);
          }
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

function delegateKey(args) {
  return String(args?.task || "").trim().split(/\s+/).slice(0, 8).join(" ");
}

function delegateDetail(args, delegated) {
  const transcript = Array.isArray(delegated.transcript) ? delegated.transcript.map((message) => {
    const parts = [`${message.role || "message"}${message.name ? ` ${message.name}` : ""}`];
    if (message.reasoning) parts.push(`thought\n${message.reasoning}`);
    if (message.tool_calls?.length) parts.push(`calls\n${JSON.stringify(message.tool_calls, null, 2)}`);
    if (message.content) parts.push(String(message.content));
    return parts.join("\n");
  }).join("\n\n") : "";
  return `arguments\n${JSON.stringify(args, null, 2)}\n\nchild transcript\n${transcript}\n\nsummary\n${delegated.summary || ""}`;
}

// Item 2lc: the rows a selection touches, each carrying ONLY the text that was
// actually selected inside it. This supersedes 2js's whole-entry rule, which was a
// deliberate choice and is now reversed — the operator: "when i copy a small bit of
// text it takes more of the window then i want. it should be exact".
export function rowsInSelection(log, selection) {
  if (!log || !selection || selection.rangeCount === 0) return [];
  const range = selection.getRangeAt(0);
  if (range.collapsed) return [];
  return [...log.querySelectorAll(".chat-entry")]
    .filter((row) => range.intersectsNode(row))
    .map((row) => {
      const text = selectedTextIn(row, range);
      // partial is the operator case: a few words inside a turn. A selection that
      // covers the whole entry is NOT partial, and keeps its mark — that is 2js's
      // whole-record copy, and it is also what stops an agent message that reads
      // "you: …" from pasting as though the operator had written it.
      return { className: row.className, text, fromSelection: true };
    })
    .filter((row) => row.text);
}

// (b): the collapsed step and fold summaries are furniture. Their caret rows —
// `steps · …`, `thought …`, `tool … → ok …` — are omitted, and their bodies are
// already hidden and omitted with them. This hides nothing: it declines to copy
// what the operator did not want.
const FURNITURE = ".chat-step-summary, .tool-tick";

// selectedTextIn is the selected text inside one entry, in document order, with
// furniture skipped and block boundaries kept as line breaks.
export function selectedTextIn(row, range) {
  const doc = row?.ownerDocument;
  // A row that cannot be walked — a detached node, or a caller that passed a plain
  // object — falls back to its own text rather than failing the copy.
  if (!doc?.createTreeWalker) return normalize(row?.innerText ?? row?.textContent ?? "");
  const walker = doc.createTreeWalker(row, 0x1 | 0x4, {
    acceptNode(node) {
      if (node.nodeType === 1) {
        if (node.hidden || (node.matches && node.matches(FURNITURE))) return 2; // FILTER_REJECT
        return 3; // FILTER_SKIP — descend, but the element itself is not text
      }
      return range.intersectsNode(node) ? 1 : 2;
    },
  });
  const lines = [];
  let block = null;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    let text = node.data ?? "";
    if (node === range.endContainer) text = text.slice(0, range.endOffset);
    if (node === range.startContainer) text = text.slice(node === range.endContainer ? range.startOffset : range.startOffset);
    if (!text) continue;
    const owner = blockOf(node, row);
    // Whitespace BETWEEN blocks is markup indentation, not content, and turning it
    // into a line of its own is what put a blank line where a step row was removed.
    // Whitespace inside a block is kept, because there it is spacing the operator
    // selected.
    if (!text.trim() && owner !== block) continue;
    if (owner !== block) { lines.push(text); block = owner; }
    else lines[lines.length - 1] += text;
  }
  return normalize(lines.join("\n"));
}

function blockOf(node, row) {
  for (let element = node.parentElement; element && element !== row.parentElement; element = element.parentElement) {
    if (["P", "DIV", "LI", "PRE", "SECTION", "HEADER", "TD", "TR"].includes(element.tagName)) return element;
  }
  return row;
}

// (d): carriage returns removed, trailing spaces trimmed, runs of blank lines
// collapsed to one, ends trimmed. The selected words themselves are never altered.
export function normalize(value) {
  return String(value ?? "").replace(/\r/g, "").replace(/[ \t]+$/gm, "").replace(/\n{3,}/g, "\n\n").trim();
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
