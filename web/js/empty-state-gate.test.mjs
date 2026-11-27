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
  const retainedHistoryGate = chat.indexOf("Number(session.history_total || 0) > 0 && !(session.chat || []).length");
  assert.ok(retainedHistoryGate > gate && retainedHistoryGate < chat.indexOf("Send a task to start the loop."), "retained history must load before the empty transcript state is shown");
  const app = await readFile(new URL("./app.js", import.meta.url), "utf8");
  assert.match(app, /store\.loaded \? "No configured agents" : ""/);
  const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
  assert.match(html, /id="activity-empty" class="panel-empty" hidden>This profile has not run yet\.<\/p>/);
  assert.match(app, /getElementById\("activity-empty"\)\.hidden = !store\.loaded \|\| !!current \|\| !ledgerAsked \|\| !!activityError/);
  // A new profile must have its own ledger result before an empty state can appear.
  assert.match(app, /if \(ledgerAgent !== askedFor \|\| store\.replay\) \{[^}]*ledgerAsked = false;/);
  assert.match(app, /const ledger = await api\(`\/api\/stats[^\n]+\);[^}]*if \(sequence !== ledgerRequest \|\| askedFor !== selectedAgent\) return;[^}]*ledgerAsked = true;/);
  assert.match(app, /!store\.loaded \? "" : !hasSelectedChat \? "no open chat"/);
});
