import assert from "node:assert/strict";
import test from "node:test";
import { approvalChoices, approvalDecisionText, approvalText, createApprovalCard, shellGrantApproval } from "./approval.js";

test("both approval kinds expose exactly chat, once, and no in order", () => {
  for (const data of [
    { name: "shell", boundary_escape: false },
    { name: "shell.operator_override", boundary_escape: true },
    { name: "shell.operator_command", boundary_escape: true },
  ]) {
    assert.equal(shellGrantApproval(data), true);
		assert.deepEqual(approvalChoices(data), [["session", "Yes, for this chat"], ["once", "Just once"], ["deny", "No"]]);
	}
});

test("file escape and policy cards stay within the same three-button vocabulary", () => {
	assert.deepEqual(approvalChoices({ name: "write_file.operator_override", boundary_escape: true }), [["session", "Yes, for this chat"], ["once", "Just once"], ["deny", "No"]]);
	assert.deepEqual(approvalChoices({ name: "write_file", boundary_escape: false }), [["session", "Yes, for this chat"], ["once", "Just once"], ["deny", "No"]]);
});

test("approval wording stays direct and identifies the operation", () => {
	const identity = approvalText({ name: "shell.operator_command", boundary_escape: true, args: { reason: "git runs as you", command: "git diff" } });
	assert.equal(identity.title, "Run as you");
	assert.match(identity.reason, /git runs as you/);
	assert.equal(identity.detail, "git diff");
	const policy = approvalText({ name: "write_file", boundary_escape: false, args: { path: "note.txt" } });
	assert.equal(policy.title, "Allow this");
	assert.equal(policy.detail, "note.txt");
});

test("connector approval shows the proposed entry verbatim", () => {
	const connector = { operation: "add", entry: { name: "deploy-broker", url: "https://broker.test/mcp", kind: "mcp", auth: "exec:helper headers" } };
	const wording = approvalText({ name: "call_service", args: { connector } });
	assert.match(wording.request, /add connector/);
	assert.deepEqual(JSON.parse(wording.detail), connector);
});

test("pending cards consume the shared human sentence order", () => {
	const wording = approvalText({
		name: "write_file",
		boundary_escape: false,
		args: { path: "note.txt" },
		human: {
			happened: "write_file needs your approval before it can continue.",
			harness_action: "The harness paused before running the action.",
			question: "Allow this action?",
		},
	});
	assert.equal(wording.request, "write_file needs your approval before it can continue.");
	assert.equal(wording.reason, "The harness paused before running the action.");
	assert.equal(wording.question, "Allow this action?");
	assert.equal(wording.detail, "note.txt");
});

test("every resolved approval is one decided line and never a pending card", () => {
	const document = {
		createTextNode: (text) => ({ text }),
		createElement: (tag) => ({ tag, children: [], className: "", classList: { add() {} }, append(...children) { this.children.push(...children); } }),
	};
	for (const decision of ["session", "once", "deny", "superseded", "dismissed"]) {
		const card = createApprovalCard(document, {
			decision,
			event: { data: { call_id: "call", name: "read_file.operator_override", boundary_escape: true, args: { path: "outside.txt" } } },
		}, { replay: true, decide: () => assert.fail("replay decision invoked") });
		assert.equal(card.className, "approval-decided");
		assert.equal(card.children.length, 0);
		assert.match(card.textContent, new RegExp(approvalDecisionText(decision)));
	}
});

test("a worker's card names the worker and nothing else changes for a chat's own card", async () => {
	const { workerApproval, sameWorkerPlan } = await import("./chat-lifecycle.js");
	const pending = { type: "notice", event: { type: "approval.required", data: { call_id: "call-1", name: "read_file.operator_override", boundary_escape: true, role: "c", plan_id: "p1" } } };
	const sessions = {
		d1: { id: "d1", role: "d", plan_id: "p1" },
		c1: { id: "c1", role: "c", plan_id: "p1", pending_approval: pending },
		c2: { id: "c2", role: "c", plan_id: "p2", pending_approval: pending },
		b1: { id: "b1", role: "b" },
	};
	assert.equal(workerApproval(sessions, sessions.d1)?.id, "c1");
	assert.equal(workerApproval(sessions, sessions.b1), null);
	assert.equal(workerApproval({ ...sessions, c1: { ...sessions.c1, closed: true } }, sessions.d1), null);
	assert.equal(sameWorkerPlan(sessions, "c1", "d1"), true);
	assert.equal(sameWorkerPlan(sessions, "c2", "d1"), false);
	const decided = [];
	const doc = fakeDocument();
	const card = createApprovalCard(doc, pending, { author: "agent_c", decide: (callID, decision) => decided.push([callID, decision]) });
	assert.equal(card.children[0].children.at(-1).textContent, "agent_c");
	assert.ok(card.className.includes("worker-approval"));
	const own = createApprovalCard(doc, pending, { decide: () => {} });
	assert.equal(own.children[0].children.length, 0);
	assert.ok(!own.className.includes("worker-approval"));
	card.children.at(-1).children[0].onclick();
	assert.deepEqual(decided, [["call-1", "session"]]);
});

function fakeDocument() {
	return {
		createElement: (tag) => {
			const node = { tag, children: [], className: "", textContent: "", append(...children) { this.children.push(...children); } };
			node.classList = { add: (name) => { node.className = `${node.className} ${name}`.trim(); } };
			return node;
		},
	};
}
