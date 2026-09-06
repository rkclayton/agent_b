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

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
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
    formatDuration: () => "1.2",
  });
  const active = { key: "turn:r1:1", reasoning: "", done: false, thinkingMS: null };

  renderer.begin();
  const first = renderer.render(active, 0);
  renderer.end();
  const firstDots = first.children[0].children[1].children[0];
  assert.equal(first.children[1].textContent, "Waiting for reasoning text…");

  renderer.begin();
  const second = renderer.render({ ...active, reasoning: "streamed thought" }, 4);
  renderer.end();
  assert.equal(second, first);
  assert.equal(second.children[0].children[1].children[0], firstDots);
  assert.equal(second.children[1].textContent, "streamed thought");

  renderer.begin();
  const completed = renderer.render({ ...active, reasoning: "streamed thought", done: true, thinkingMS: 1200 }, 4);
  renderer.end();
  assert.equal(completed, first);
  assert.equal(completed.children[0].children[2].textContent, "Thought 1.2 seconds (4 tokens)");

  renderer.begin();
  const unavailable = renderer.render({ ...active, done: true, thinkingMS: 1200 }, 4);
  renderer.end();
  assert.equal(unavailable.children[1].textContent, "Reasoning text is unavailable in this recording.");
});
