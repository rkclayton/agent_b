import assert from "node:assert/strict";
import test from "node:test";

globalThis.document = { hidden: false, getElementById: () => null, addEventListener: () => {} };
globalThis.window = { addEventListener: () => {} };
globalThis.EventSource = class { addEventListener() {} };
const { reduce, store } = await import("./bus.js");

function snapshot(session = {}) {
  reduce({ type: "snapshot", data: { sessions: { main: { id: "main", cursor: { generation: "g", offset: 10 }, run: { status: "idle" }, tools: [], messages: [], timeline: [], ...session } }, replay: false, servers: [], config: {} } });
}
function patch(offset, operations, previous = offset - 1) {
  reduce({ type: "projection.patch", data: { schema_version: 1, session_id: "main", previous_cursor: { generation: "g", offset: previous }, cursor: { generation: "g", offset }, operations } });
}

test("browser applies authoritative replace and append operations", () => {
  snapshot({ model_turns: 4 });
  patch(11, [{ op: "replace", path: "/model_turns", value: 5 }], 10);
  patch(12, [{ op: "append", path: "/timeline", value: { type: "model.response", data: { turn: 5 } } }], 11);
  assert.equal(store.sessions.main.model_turns, 5);
  assert.equal(store.sessions.main.timeline.length, 1);
  assert.equal(store.sessions.main.cursor.offset, 12);
});

test("projection replacement cannot merge stale budget/tool fields", () => {
  snapshot({ tools: [{ name: "removed", marginal_tokens: 145 }], budget: { tool_marginal_tokens: { removed: 145 } } });
  patch(11, [
    { op: "replace", path: "/tools", value: [{ name: "read_file", marginal_tokens: 20 }] },
    { op: "replace", path: "/budget", value: { categories: { tools: 100 }, tool_marginal_tokens: { read_file: 20 } } },
  ], 10);
  assert.deepEqual(store.sessions.main.tools.map((tool) => tool.name), ["read_file"]);
  assert.equal(store.sessions.main.budget.tool_marginal_tokens.removed, undefined);
});

test("mutable messages and immutable History records arrive as separate projected fields", () => {
  snapshot();
  const original = { type: "message.appended", data: { message: { id: "result-1", content: "complete tool result" } } };
  patch(11, [
    { op: "replace", path: "/messages", value: [{ id: "result-1", content: "[elided: complete tool result]", elided: true }] },
    { op: "replace", path: "/timeline", value: [original] },
  ], 10);
  assert.equal(store.sessions.main.messages[0].content, "[elided: complete tool result]");
  assert.equal(store.sessions.main.timeline[0].data.message.content, "complete tool result");
});
