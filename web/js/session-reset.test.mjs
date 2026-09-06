import assert from "node:assert/strict";
import test from "node:test";

import { createSessionResetController } from "./session-reset.js";

class FakeButton {
  constructor() {
    this.attributes = new Map();
    this.dataset = {};
    this.listeners = new Map();
  }
  setAttribute(name, value) { this.attributes.set(name, String(value)); }
  addEventListener(type, listener) { this.listeners.set(type, listener); }
  click() { return this.listeners.get("click")(); }
}

test("clear conversation keeps confirmation and force-stops an active run", async () => {
  const button = new FakeButton();
  const confirmations = [];
  const resets = [];
  createSessionResetController(button, {
    session: () => ({ id: "main", label: "work", run: { status: "running" } }),
    confirmClear: (text) => { confirmations.push(text); return true; },
    reset: async (...args) => resets.push(args),
    reportError: assert.fail,
  });

  await button.click();
  assert.match(confirmations[0], /permanently clears its messages and run counters/i);
  assert.match(confirmations[0], /workspace files, profile, enabled tools, and memory remain/i);
  assert.deepEqual(resets, [["main", true]]);
});

test("declining clear conversation makes no request", async () => {
  const button = new FakeButton();
  let requests = 0;
  createSessionResetController(button, {
    session: () => ({ id: "main", run: { status: "idle" } }),
    confirmClear: () => false,
    reset: async () => { requests++; },
    reportError: assert.fail,
  });
  await button.click();
  assert.equal(requests, 0);
});
