import assert from "node:assert/strict";
import test from "node:test";

import { renderMarkdown } from "./markdown.js";

class FakeNode {
  constructor(ownerDocument, tagName = "") {
    this.ownerDocument = ownerDocument;
    this.tagName = tagName;
    this.children = [];
    this._text = "";
  }

  append(...nodes) {
    this.children.push(...nodes);
  }

  replaceChildren(...nodes) {
    this.children = [...nodes];
    this._text = "";
  }

  set textContent(value) {
    this.children = [];
    this._text = String(value);
  }

  get textContent() {
    return this._text + this.children.map((child) => child.textContent).join("");
  }
}

class FakeDocument {
  createElement(tagName) {
    return new FakeNode(this, tagName);
  }

  createTextNode(text) {
    const node = new FakeNode(this);
    node.textContent = text;
    return node;
  }
}

test("successive cumulative stream renders replace rather than append", () => {
  const document = new FakeDocument();
  const root = new FakeNode(document, "div");

  renderMarkdown(root, "Hey! Not");
  renderMarkdown(root, "Hey! Not much — just");
  renderMarkdown(root, "Hey! Not much — just here in the console.");

  assert.equal(root.children.length, 1);
  assert.equal(root.textContent, "Hey! Not much — just here in the console.");
});
