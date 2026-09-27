import assert from "node:assert/strict";
import test from "node:test";

import { WAIT_CELLS, setWaitProgress, waitElement } from "./wait.js";

// A DOM small enough to be obviously correct: the element builds one node tree
// and the assertions are about what is in it.
function fakeDocument() {
  const make = (tag) => {
    const node = {
      tag, children: [], className: "", textContent: "", dataset: {}, attributes: {},
      style: { values: {}, setProperty(k, v) { this.values[k] = v; }, removeProperty(k) { delete this.values[k]; } },
      append(...kids) { this.children.push(...kids); },
      setAttribute(name, value) { this.attributes[name] = value; },
      removeAttribute(name) { delete this.attributes[name]; },
      querySelectorAll(selector) {
        const want = selector.replace(".", "");
        const out = [];
        const walk = (n) => { for (const kid of n.children) { if (kid.className.split(" ").includes(want)) out.push(kid); walk(kid); } };
        walk(this);
        out.forEach = Array.prototype.forEach.bind(out);
        return out;
      },
    };
    return node;
  };
  return { createElement: make };
}

test("an element cannot be built without saying what is being waited for", () => {
  // (b): an animation alone is decoration, and a reader watching a decoration
  // cannot tell a slow model from a hung one.
  assert.throws(() => waitElement(fakeDocument(), {}), /must say what is being waited for/);
  assert.throws(() => waitElement(fakeDocument(), { line: "   " }), /must say what is being waited for/);
});

test("with nothing reporting progress it is indeterminate and claims no position", () => {
  // (c): an indeterminate wait never pretends to be determinate.
  const element = waitElement(fakeDocument(), { line: "waiting for the model" });
  assert.equal(element.dataset.mode, "indeterminate");
  assert.equal(element.attributes["aria-valuenow"], undefined);
  assert.equal(element.querySelectorAll(".wait-cell").length, WAIT_CELLS);
});

test("with real progress it fills exactly as far as it was told", () => {
  const element = waitElement(fakeDocument(), { line: "reading the prompt", processed: 250, total: 1000 });
  assert.equal(element.dataset.mode, "determinate");
  assert.equal(element.attributes["aria-valuenow"], "25");
  const lit = element.querySelectorAll(".wait-cell").filter((cell) => cell.dataset.lit === "1").length;
  assert.equal(lit, Math.round(0.25 * WAIT_CELLS));
});

test("a server that stops reporting progress returns to indeterminate", () => {
  // Rather than freezing a bar that will never move again.
  const element = waitElement(fakeDocument(), { line: "reading the prompt", processed: 900, total: 1000 });
  assert.equal(element.dataset.mode, "determinate");
  setWaitProgress(element, { processed: null, total: null });
  assert.equal(element.dataset.mode, "indeterminate");
  assert.equal(element.attributes["aria-valuenow"], undefined);
  assert.equal(element.querySelectorAll(".wait-cell").every((cell) => cell.dataset.lit === undefined), true);
});
