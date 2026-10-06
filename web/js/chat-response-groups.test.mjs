import assert from "node:assert/strict";
import test from "node:test";

import { groupResponseRows, hasVisibleChatContent, isIdenticalSingleStepFold, isThinThought, responseBlocks, responseHasOnlyThoughts, responseStepOutcome, responseSummary, shortToolTarget, thinThoughtTokenLimit } from "./chat-response-groups.js";

const tool = (key, name, ok = true, ms = 1) => ({ type: "tool", key, name, args: {}, result: { ok, ms } });
const thought = (key, tokens, text = "") => ({ type: "agent", key, reasoning: "x", reasoningTokens: tokens, text, done: true, thinkingMS: 2 });

test("Chat ports adjacent grouping and absorbs only fixed-token thin thoughts", () => {
  assert.equal(isThinThought(thought("limit", thinThoughtTokenLimit)), true);
  assert.equal(isThinThought(thought("over", thinThoughtTokenLimit + 1)), false);
  const rows = groupResponseRows([
    tool("r1", "read_file"), thought("thin", 12), tool("r2", "read_file", false, 3),
    thought("long", 65), tool("r3", "read_file"), tool("s1", "shell"), tool("r4", "read_file"),
  ]);
  assert.deepEqual(rows.map((row) => row.kind || row.type), ["tool-group", "agent", "tool", "tool", "tool"]);
  assert.deepEqual(rows[0], {
    kind: "tool-group", key: "tool-group:r1", tool: "read_file", items: [tool("r1", "read_file"), thought("thin", 12), tool("r2", "read_file", false, 3)],
    calls: 2, thoughts: 1, failed: 1, duration: 6,
  });
});

test("collapsed turn arithmetic exposes failures and preserves every item", () => {
  const items = [thought("t1", 8), tool("a", "read_file", false, 7), { type: "notice", key: "n", event: { type: "run.aborted", data: {} } }, { type: "agent", key: "answer", text: "done", done: true }];
  assert.deepEqual(responseSummary(items), { tools: 1, thoughts: 1, answers: 1, failed: 1, duration: 9 });
  const expanded = groupResponseRows([tool("a", "read_file"), thought("t2", 4), tool("b", "read_file")])[0].items;
  assert.equal(expanded.length, 3);
  assert.deepEqual(expanded.map((item) => item.key), ["a", "t2", "b"]);
});

test("only an explicit failed tool result counts as a tool failure", () => {
  assert.equal(responseSummary([{ type: "tool", key: "bad", name: "read_file" }]).failed, 0);
  assert.equal(responseSummary([{ type: "notice", key: "bad-render", event: { type: "error", data: { where: "ui" } } }]).failed, 0);
  assert.equal(responseSummary([tool("failed", "read_file", false)]).failed, 1);
});

test("2ql recovered failures are retries and only a terminal failed group alarms", () => {
  const failed = tool("failed", "shell", false);
  const recovered = [failed, tool("retry", "shell", true)];
  assert.deepEqual(responseStepOutcome(recovered), { alarm: false, retried: 1 });
  assert.deepEqual(responseStepOutcome([failed], { movedPast: true }), { alarm: false, retried: 1 });
  assert.deepEqual(responseStepOutcome([failed]), { alarm: true, retried: 0 });
});

test("2ql collapsed tool rows use a short target and retain no full command", () => {
  assert.equal(shortToolTarget("shell", { command: "git stash pop --index and-everything-after" }), "git stash pop");
  assert.equal(shortToolTarget("read_file", { path: "C:\\work\\reports\\result.txt" }), "result.txt");
  assert.equal(shortToolTarget("fetch_url", { url: "https://docs.example.test/long/path?q=secret" }), "docs.example.test");
});

test("response blocks keep prose visible and assign only their following steps", () => {
  const items = [
    thought("leading", 8),
    { type: "agent", key: "first", text: "First prose", reasoning: "first thought", reasoningTokens: 3, done: true },
    tool("after-first", "read_file"),
    { type: "notice", key: "notice-first", event: { type: "compaction", data: {} } },
    { type: "agent", key: "second", text: "Second prose", reasoning: "second thought", reasoningTokens: 4, done: true },
    tool("after-second", "shell"),
  ];
  const blocks = responseBlocks(items);
  assert.deepEqual(blocks.map((block) => ({
    key: block.key,
    prose: block.prose?.text || "",
    steps: block.steps.map((item) => item.key),
  })), [
    // Item 2mu (a): a leading thought carries the same `thought:` key it will
    // carry once prose arrives, so the fold the operator opened survives the
    // switch between branches. Before this it was keyed "leading" here and
    // "thought:leading" there, and that change is what shut the fold.
    { key: "response-block:leading:leading", prose: "", steps: ["thought:leading"] },
    { key: "response-block:first", prose: "First prose", steps: ["thought:first", "after-first", "notice-first"] },
    { key: "response-block:second", prose: "Second prose", steps: ["thought:second", "after-second"] },
  ]);
  assert.equal(blocks[1].steps[0].text, "");
  assert.equal(blocks[1].steps[0].reasoning, "first thought");
});

test("tool-only responses create no empty prose region", () => {
  const items = [tool("only", "recall")];
  const blocks = responseBlocks(items);
  assert.equal(blocks.length, 1);
  assert.equal(blocks[0].prose, null);
  assert.deepEqual(blocks[0].steps.map((item) => item.key), ["only"]);
  assert.equal(isIdenticalSingleStepFold(items, blocks), true);
});

test("only agent reasoning and prose qualify for the direct thought layout", () => {
  assert.equal(responseHasOnlyThoughts([thought("one", 12, "answer")]), true);
  assert.equal(responseHasOnlyThoughts([thought("one", 12), { type: "agent", key: "answer", text: "done", done: true }]), true);
  assert.equal(responseHasOnlyThoughts([thought("one", 12), tool("call", "read_file")]), false);
  assert.equal(responseHasOnlyThoughts([]), false);
});

test("distinct response and step record sets keep both folds", () => {
  const items = [
    { type: "agent", key: "answer", text: "progress", reasoning: "why", reasoningTokens: 5, done: true },
    tool("after", "read_file"),
  ];
  const blocks = responseBlocks(items);
  assert.equal(isIdenticalSingleStepFold(items, blocks), false);
  assert.deepEqual(responseSummary(items), { tools: 1, thoughts: 1, answers: 1, failed: 0, duration: 1 });
  assert.deepEqual(responseSummary(blocks[0].steps), { tools: 1, thoughts: 1, answers: 0, failed: 0, duration: 1 });
});

test("only completed agent entries with no content are omitted from Chat", () => {
  assert.equal(hasVisibleChatContent({ type: "agent", key: "empty", done: true }), false);
  assert.equal(hasVisibleChatContent({ type: "agent", key: "reasoning", reasoning: "thinking", done: true }), true);
  assert.equal(hasVisibleChatContent({ type: "agent", key: "answer", text: "done", done: true }), true);
  assert.equal(hasVisibleChatContent({ type: "agent", key: "streaming", done: false }), true);
  assert.equal(hasVisibleChatContent({ type: "tool", key: "tool" }), true);
  assert.equal(hasVisibleChatContent({ type: "notice", key: "notice" }), true);
  assert.equal(hasVisibleChatContent({ type: "notice", key: "empty-delivery", event: { type: "files.delivered", data: { items: [] } } }), false);
  assert.equal(hasVisibleChatContent({ type: "notice", key: "delivery", event: { type: "files.delivered", data: { items: [{}] } } }), true);
  assert.equal(hasVisibleChatContent({ type: "notice", key: "malformed-delivery", event: { type: "files.delivered", data: {} } }), true);
  assert.equal(hasVisibleChatContent({ type: "notice", key: "routine-done", event: { type: "run.stopped", data: { reason: "done" } } }), false);
  assert.equal(hasVisibleChatContent(null), true);
});

// Item 2mu's W0: CONFIRM OR REFUTE THE CHANGING-KEY THEORY BY OBSERVATION before
// touching anything. The operator, 2026-09-27 03:10: "when the agent thinks you
// still cant expand the carrot — it flashes if you try but nothing."
//
// The theory is confirmed, and the key changes for a reason the item did not name:
// not per delta, but at the FIRST TEXT DELTA. One agent item streaming reasoning
// with no text yet falls through to the leading block and keeps its own key; the
// moment that same item gains any text it takes the prose branch and its thought
// is split out under a `thought:` prefix. reasoning.js keys its views and its
// `expanded` set by exactly that key and drops every view whose key went unused in
// a pass — so the fold the operator opened is looked up under a key that no longer
// exists, renders shut, and its view is discarded. One frame open, then shut.
test("a live thought keeps one key from its first delta to its last", () => {
  const keysFor = (item) => responseBlocks([item]).flatMap((block) => block.steps.map((step) => step.key));

  // Streaming: reasoning has arrived, text has not.
  const thinking = { type: "agent", key: "turn:r643:1", reasoning: "weighing it up", text: "", done: false };
  const whileThinking = keysFor(thinking);
  assert.deepEqual(whileThinking, ["thought:turn:r643:1"], "the live thought's key");

  // The same item, one text delta later. THIS is the key that used to change.
  const answering = { ...thinking, text: "Here is" };
  assert.deepEqual(keysFor(answering), whileThinking, "the key changed when the first text delta arrived, which is the flash");

  // And through to done.
  assert.deepEqual(keysFor({ ...answering, text: "Here is the answer.", done: true }), whileThinking);
});
