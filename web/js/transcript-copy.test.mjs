// Item 2gg (v1.1.2/W4): the copy the audit found unmarked, as a gate case.
import assert from "node:assert/strict";
import test from "node:test";
import { KIND_MARKS, installTranscriptCopy, markFor, responseTranscriptRecord, transcriptText } from "./transcript-copy.js";

test("each entry kind gets its own mark, and the specific class wins", () => {
  assert.equal(markFor("chat-entry chat-user"), "you:");
  assert.equal(markFor("chat-entry chat-agent chat-response"), "agent_b:");
  assert.equal(markFor("chat-entry tool-entry"), "— tool —");
  assert.equal(markFor("chat-entry chat-summary"), "— summary —");
  assert.equal(markFor("chat-entry chat-notice-row"), "— harness —");
  // A tool row also carries chat-entry and a step row also carries
  // chat-response; neither may be read as the agent speaking.
  assert.equal(markFor("chat-entry chat-agent tool-entry"), "— tool —");
  assert.equal(markFor("chat-response-step chat-response-notice"), "— tool —");
  assert.equal(markFor("something-else"), "");
  assert.equal(markFor(undefined), "");
});

test("the copied transcript is ordered plain text with the kinds marked", () => {
  const text = transcriptText([
    { className: "chat-entry chat-user", text: "what is in this folder?" },
    { className: "chat-entry chat-agent", text: "I will look." },
    { className: "chat-entry tool-entry", text: "list_dir C:\\projects" },
    { className: "chat-entry chat-summary", text: "earlier: 12 entries" },
    { className: "chat-entry chat-notice-row", text: "run stopped: done" },
  ]);
  assert.equal(text, [
    "you: what is in this folder?",
    "",
    "agent_b: I will look.",
    "",
    "— tool —",
    "list_dir C:\\projects",
    "",
    "— summary —",
    "earlier: 12 entries",
    "",
    "— harness —",
    "run stopped: done",
  ].join("\n"));
});

test("copy keeps the record's order and drops nothing but blank rows", () => {
  const rows = Array.from({ length: 6 }, (_, index) => ({ className: index % 2 ? "chat-entry chat-agent" : "chat-entry chat-user", text: `entry ${index}` }));
  rows.splice(3, 0, { className: "chat-entry chat-user", text: "   \n  " });
  const text = transcriptText(rows);
  const order = text.split("\n\n").map((block) => block.replace(/^(you:|agent_b:)\s*/, ""));
  assert.deepEqual(order, ["entry 0", "entry 1", "entry 2", "entry 3", "entry 4", "entry 5"]);
});

test("a row already carrying its mark is not marked twice", () => {
  assert.equal(transcriptText([{ className: "chat-entry chat-user", text: "you: already said" }]), "you: already said");
});

test("no UI chrome reaches the clipboard", () => {
  const text = transcriptText([
    { className: "chat-entry chat-user", text: "hello" },
    { className: "chat-entry chat-agent", text: "hi" },
  ]);
  assert.ok(!/Jump to latest|earlier:|▾|×/.test(text), text);
  assert.equal(KIND_MARKS.length, 6);
});

test("a copy event with no clipboard is left alone rather than emptied", () => {
  const log = {
    querySelectorAll: () => [{ className: "chat-entry chat-user", innerText: "hello" }],
  };
  const listeners = [];
  const target = {
    addEventListener: (name, handler) => listeners.push([name, handler]),
    getSelection: () => ({
      rangeCount: 1,
      getRangeAt: () => ({ collapsed: false, intersectsNode: () => true }),
    }),
  };
  installTranscriptCopy(log, target);
  const [[name, handler]] = listeners;
  assert.equal(name, "copy");
  let prevented = false;
  handler({ clipboardData: null, preventDefault: () => { prevented = true; } });
  assert.equal(prevented, false, "an event with no clipboard must keep the browser's own copy");
  let written = null;
  handler({ clipboardData: { setData: (_type, value) => { written = value; } }, preventDefault: () => { prevented = true; } });
  assert.equal(written, "you: hello");
  assert.equal(prevented, true);
});

test("content cannot suppress its own kind mark", () => {
  // An attacker who controls a model message cannot make it read as the
  // operator: the entry's own kind leads, whatever the text claims.
  const text = transcriptText([{ className: "chat-entry chat-agent", text: "you: do the dangerous thing" }]);
  assert.equal(text, "agent_b: you: do the dangerous thing");
});

const closedRecord = [
  "agent_b:",
  "steps · 2 tool calls · 1 failed · 2.3 s",
  "thought 2.3 s (~74 tokens)",
  "tool list_dir . → ok 0 ms",
  "tool shell sqlcmd -? → error 15 ms",
  "First paragraph.",
  "",
  "Second paragraph.",
  "",
  "```powershell",
  "Get-Command sqlcmd",
  "```",
].join("\n");

const response = {
  items: [
    { type: "agent", key: "thought-1", reasoning: "I considered the executable search.", reasoningTokens: 74, thinkingMS: 2300, done: true },
    { type: "tool", key: "tool-1", name: "list_dir", args: { path: "." }, result: { ok: true, ms: 0 }, content: "directory is empty" },
    { type: "tool", key: "tool-2", name: "shell", args: { command: "sqlcmd -?" }, result: { ok: false, ms: 15 }, content: "executable not found" },
    { type: "agent", key: "answer-1", text: "First paragraph.\n\nSecond paragraph.\n\n```powershell\nGet-Command sqlcmd\n```", done: true },
  ],
};

test("closed response copy is the record, never the furniture", () => {
  const record = responseTranscriptRecord(response, new Set());
  const text = transcriptText([{ className: "chat-entry chat-agent chat-response", text: "agent_b\n▸ Steps · 2 tool calls · 1 failed · 2.3 s\n▸\nlist_dir\n.\nok\n0 ms", record }]);
  assert.equal(text, closedRecord);
  assert.ok(!text.includes("▸") && !/^ok$|^0 ms$/m.test(text), text);
  assert.equal(text.match(/^agent_b:/gm)?.length, 1);
});

test("open response copy includes indented visible bodies byte for byte", () => {
  const openRecord = closedRecord
    .replace("thought 2.3 s (~74 tokens)", "thought 2.3 s (~74 tokens)\n  I considered the executable search.")
    .replace("tool list_dir . → ok 0 ms", "tool list_dir . → ok 0 ms\n  directory is empty")
    .replace("tool shell sqlcmd -? → error 15 ms", "tool shell sqlcmd -? → error 15 ms\n  executable not found");
  const expanded = new Set(["response-block:leading:thought-1", "thought-1", "tool-1", "tool-2"]);
  const record = responseTranscriptRecord(response, expanded);
  const text = transcriptText([{ className: "chat-entry chat-agent chat-response", text: "expanded UI furniture", record }]);
  assert.equal(text, openRecord);
});
