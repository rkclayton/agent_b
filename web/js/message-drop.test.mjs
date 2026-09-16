import assert from "node:assert/strict";
import test from "node:test";
import { createMessageDropController } from "./message-drop.js";

function button() {
  return { disabled: false, dataset: {}, title: "", attributes: {}, listener: null,
    setAttribute(name, value) { this.attributes[name] = value; },
    addEventListener(_name, listener) { this.listener = listener; } };
}

test("drop last message confirms the exact destructive action", async () => {
  const control = button();
  const session = { id: "main", label: "Main", run: { status: "idle" }, messages: [{ id: "m2", role: "tool" }] };
  let prompt = "";
  let dropped = "";
  createMessageDropController(control, {
    session: () => session,
    confirmDrop: (value) => { prompt = value; return true; },
    drop: async (id) => { dropped = id; },
    reportError: assert.fail,
  });
  await control.listener();
  assert.equal(dropped, "main");
  assert.match(prompt, /most recent tool message/);
  assert.match(prompt, /cannot be undone/i);
});

test("drop last message is disabled without an idle message", () => {
  const control = button();
  const session = { id: "main", run: { status: "running" }, messages: [{ id: "m1", role: "user" }] };
  const controller = createMessageDropController(control, {
    session: () => session, confirmDrop: () => true, drop: async () => {}, reportError: assert.fail,
  });
  assert.equal(control.disabled, true);
  session.run.status = "idle";
  session.messages = [];
  controller.render();
  assert.equal(control.disabled, true);
});
