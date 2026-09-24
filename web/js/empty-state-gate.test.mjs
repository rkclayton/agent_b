import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

globalThis.document = { hidden: false, getElementById: () => null, addEventListener: () => {} };
globalThis.window = { addEventListener: () => {} };
const stored = new Map();
globalThis.sessionStorage = { getItem: (key) => stored.get(key) || null, setItem: (key, value) => stored.set(key, value) };
globalThis.EventSource = class { addEventListener() {} };
const { reduce, store } = await import("./bus.js");

// Item 2ew: nothing is known until the first full snapshot, so no empty-state
// text may be drawn before it.
test("the store is not loaded until the first snapshot is applied", () => {
  assert.equal(store.loaded, false);
  reduce({ type: "projection.patch", data: { schema_version: 1, session_id: "x", previous_cursor: { generation: "", offset: 0 }, cursor: { generation: "g", offset: 1 }, operations: [] } });
  assert.equal(store.loaded, false);
  reduce({ type: "snapshot", data: { sessions: {}, connections: [], config: { agents: [{ name: "a" }] }, replay: false } });
  assert.equal(store.loaded, true);
});

test("chat and console empty states are gated on the first snapshot or the ledger", async () => {
  const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
  const gate = chat.indexOf("if (!store.loaded) {");
  assert.ok(gate > 0 && gate < chat.indexOf("No agent connected") && gate < chat.indexOf("Send a task to start the loop."), "chat empty states must follow the loaded gate");
  const app = await readFile(new URL("./app.js", import.meta.url), "utf8");
  assert.match(app, /store\.loaded \? "No configured agents" : ""/);
  assert.match(app, /store\.loaded && ledgerAsked \? '<p class="panel-empty">No lifetime activity\.<\/p>' : ""/);
  // An in-flight fetch never says "no activity": the flag clears before the request.
  assert.match(app, /ledgerAsked = false;\s+try \{ ledger = await api/);
  assert.match(app, /!store\.loaded \? "" : !hasSelectedChat \? "no open chat"/);
});
