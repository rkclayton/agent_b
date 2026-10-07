import assert from "node:assert/strict";
import test from "node:test";

import { agentAuthor, chatActivityText, chatActivityTitle, chatName, chatRowText, openSessions, sessionTitle } from "./chat-lifecycle.js";

const idle = {
  id: "s2",
  agent_name: "Coder",
  b_connection: "Acme LAB",
  created_at: "2026-09-07T12:00:00Z",
  closed: false,
  run: { status: "idle" },
  timeline: [
    { type: "run.started", run_id: "r1" },
    { type: "run.stopped", run_id: "r1", ts: "2026-09-22T16:47:00Z" },
    { type: "run.started", run_id: "r2" },
  ],
  chat: [
    { type: "user", text: "Summarize the attached contract\nKeep headings." },
    { type: "tool", name: "write_file", args: { path: "notes.md" }, result: { ok: true } },
    { type: "tool", name: "edit_file", args: { path: "todo.md" }, result: { ok: true } },
    { type: "notice", event: { type: "memory.noted" } },
  ],
};

// Item 2go (v1.2.5), the operator: "i want it to display like this:
// MM:DD · Chat name · × , nothing more."
test("Chat row is only the chat's truthful name", () => {
	assert.equal(chatRowText({ ...idle, label: "summarize 2 contracts" }), "summarize 2 contracts");
	assert.equal(chatRowText(idle), "Summarize the attached contract", "an unnamed chat uses its first user line");
	assert.equal(chatName({ ...idle, label: "null" }), "Summarize the attached contract");
	assert.equal(chatName({ ...idle, label: null, chat: [] }), "New chat");
});

test("Header activity uses the product clock and supplies full hover text", () => {
	const now = new Date(2026, 9, 7, 18, 0), today = { last_activity: new Date(2026, 9, 7, 13, 5).toISOString() };
	const earlier = { last_activity: new Date(2026, 9, 6, 13, 5).toISOString() };
	assert.equal(chatActivityText(today, now), new Date(today.last_activity).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }));
	assert.match(chatActivityText(earlier, now), /\d.*\d/);
	assert.equal(chatActivityText({}, now), "");
	assert.equal(chatActivityTitle(today), new Date(today.last_activity).toLocaleString());
});

test("Open chat list excludes durable closed sessions", () => {
  assert.deepEqual(openSessions({ s2: idle, s3: { ...idle, id: "s3", closed: true } }).map((session) => session.id), ["s2"]);
});

test("Assistant labels use only role and the header carries only role and connection", () => {
  assert.equal(agentAuthor(idle), "agent_b");
  assert.equal(agentAuthor(idle, "c"), "agent_c");
  assert.equal(sessionTitle(idle), "Acme LAB", "item 2eo: the header reads the connection name only");
  assert.equal(sessionTitle({ ...idle, role: "d", plan_name: "Release map", workspace: "C:\\code\\acme" }), "Acme LAB");
});
