import assert from "node:assert/strict";
import test from "node:test";

import { createThinkingRenderer } from "./reasoning.js";

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName;
    this.children = [];
    this.attributes = new Map();
    this.className = "";
    this.hidden = false;
    this.textContent = "";
  }

  append(...children) {
    this.children.push(...children);
  }

  addEventListener(name, handler) {
    (this.listeners ||= new Map()).set(name, handler);
  }

  removeAttribute(name) {
    this.attributes.delete(name);
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  hasAttribute(name) {
    return this.attributes.has(name);
  }
}

const fakeDocument = { createElement: (tagName) => new FakeElement(tagName) };

test("thinking renderer preserves its DOM and never opens to an empty body", () => {
  const expanded = new Set(["turn:r1:1"]);
  const renderer = createThinkingRenderer({
    document: fakeDocument,
    expanded,
    rerender: () => {},
    format: String,
    formatDuration: (value) => value === undefined || value === null ? "" : "1.2",
  });
  const active = { key: "turn:r1:1", reasoning: "", done: false, thinkingMS: null };

  renderer.begin();
  const first = renderer.render(active, 0);
  renderer.end();
  // children: [caret, thought-glyph, "Thinking", <em> summary]. The glyph is one
  // node reused across renders, which is what "preserves its DOM" means here.
  const firstGlyph = first.children[0].children[1];
  const body = first.children.find((child) => child.className === "thinking-body");
  const collapse = first.children.find((child) => child.className === "collapse-arrow");
  assert.equal(body.textContent, "Waiting for reasoning text…");
  assert.equal(collapse.hidden, false);

  renderer.begin();
  const second = renderer.render({ ...active, reasoning: "streamed thought" }, 4);
  renderer.end();
  assert.equal(second, first);
  assert.equal(second.children[0].children[1], firstGlyph);
  assert.equal(firstGlyph.className, "thought-glyph");
  assert.equal(body.textContent, "streamed thought");

  renderer.begin();
  const completed = renderer.render({ ...active, reasoning: "streamed thought", done: true, thinkingMS: 1200 }, 4);
  renderer.end();
  assert.equal(completed, first);
  assert.equal(completed.children[0].children[3].textContent, "Thought 1.2 (4 tokens)");

  renderer.begin();
  const noDuration = renderer.render({ ...active, reasoning: "streamed thought", done: true, thinkingMS: null }, 4);
  renderer.end();
  assert.equal(noDuration.children[0].children[3].textContent, "Thought (4 tokens)");

  const inlineRenderer = createThinkingRenderer({
    document: fakeDocument,
    expanded: new Set(),
    rerender: () => {},
    format: String,
    formatDuration: () => "1.2",
    uncounted: () => true,
  });
  inlineRenderer.begin();
  const inline = inlineRenderer.render({ ...active, reasoning: "fragment", done: true, thinkingMS: 1200 }, 4);
  inlineRenderer.end();
  assert.equal(inline.children[0].children[3].textContent, "Thought 1.2 (thoughts uncounted)");

  renderer.begin();
  const unavailable = renderer.render({ ...active, done: true, thinkingMS: 1200 }, 4);
  renderer.end();
  assert.equal(body.textContent, "Reasoning text is unavailable in this recording.");
  collapse.onclick();
  renderer.begin();
  renderer.render({ ...active, done: true, thinkingMS: 1200 }, 4);
  renderer.end();
  // Item 2gg: a collapsed thought is hidden "until-found", so the browser's
  // own find still reaches the model output inside it.
  assert.equal(body.getAttribute("hidden"), "until-found");
  assert.equal(collapse.hidden, true);
});

// Item 2mu (c) and (d): NO RENDER PASS MAY CLOSE A FOLD THE OPERATOR OPENED.
//
// This is the operator's symptom, driven the way he drove it: open the fold on a
// live thought, then feed deltas — including the first TEXT delta, which is where
// the key used to change and the fold used to shut — and check it is still open on
// every pass, then through the done transition.
//
// The renderer drops every view whose key went unused in a pass, so a key that
// changes mid-stream both closes the fold and discards its DOM. That is why this
// asserts on each pass rather than only at the end.
test("a fold opened while the model is thinking stays open through every delta and the done transition", () => {
  const expanded = new Set();
  const renderer = createThinkingRenderer({
    document: fakeDocument,
    expanded,
    rerender: () => {},
    format: String,
    formatDuration: (value) => value === undefined || value === null ? "" : "1.2",
  });

  // The key the grouping now gives a thought, live or done alike.
  const key = "thought:turn:r643:1";
  const pass = (entry, tokens) => {
    renderer.begin();
    const root = renderer.render(entry, tokens);
    renderer.end();
    return root;
  };
  // children[0] is the thinking line; its children[1] is the glyph that carries
  // data-open, and the line itself carries aria-expanded. Both are checked,
  // because the operator sees the glyph and a screen reader sees the attribute.
  const openState = (root) => ({
    glyph: root.children[0].children[1].getAttribute("data-open"),
    aria: root.children[0].getAttribute("aria-expanded"),
  });

  // One pass before the operator touches it, so the view exists.
  const first = pass({ key, reasoning: "weigh", done: false, thinkingMS: null }, 2);
  assert.deepEqual(openState(first), { glyph: "false", aria: "false" });

  // He clicks the caret. That is what the renderer's own click handler does.
  expanded.add(key);

  // Now the stream continues, and the first text delta arrives partway through.
  const deltas = [
    { reasoning: "weighing it", text: "", done: false },
    { reasoning: "weighing it up", text: "", done: false },
    { reasoning: "weighing it up carefully", text: "Here is", done: false },
    { reasoning: "weighing it up carefully now", text: "Here is the", done: false },
  ];
  for (const [index, delta] of deltas.entries()) {
    const root = pass({ key, ...delta, thinkingMS: null }, 4 + index);
    assert.deepEqual(openState(root), { glyph: "true", aria: "true" }, `the fold closed on delta ${index + 1}, which is the flash`);
    // And the same DOM, not a re-mount: a discarded view loses the operator's place.
    assert.equal(root, first, `the view was re-mounted on delta ${index + 1}`);
  }

  // (d): the done transition keeps it open.
  const done = pass({ key, reasoning: "weighing it up carefully now", text: "Here is the answer.", done: true, thinkingMS: 2300 }, 74);
  assert.deepEqual(openState(done), { glyph: "true", aria: "true" }, "the fold closed when the thought completed");
  assert.equal(done, first);

  // And a fold the operator closes stays closed through the same traffic.
  expanded.delete(key);
  const closed = pass({ key, reasoning: "more", text: "Here is the answer.", done: true, thinkingMS: 2300 }, 80);
  assert.deepEqual(openState(closed), { glyph: "false", aria: "false" }, "a fold the operator closed reopened itself");
});
