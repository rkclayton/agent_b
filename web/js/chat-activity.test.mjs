import assert from "node:assert/strict";
import test from "node:test";

import { liveActivityText, modelRequestText, showsStreamCaret } from "./chat-activity.js";

const running = (activity) => ({ run: { status: "running" }, activity });

test("live activity gives each model phase a measured status", () => {
  assert.equal(liveActivityText(running({ stage: "call_model", stage_state: "enter", progress: { processed: 2868 }, stream: {} })), "waiting for first token — prompt 2.9k · 0s");
  assert.equal(modelRequestText({ stream: { reasoning_chars: 605, total_chars: 605 } }), "thinking · 169 tokens");
  assert.equal(modelRequestText({ stream: { reasoning_chars: 605, total_chars: 750 } }), "writing · 41 tokens");
  assert.equal(modelRequestText({ stream: { tool_calls: [{ name: "run_script", argument_bytes: 12400, started_at: 1000, last_chunk_at: 131000 }] } }), "calling run_script · 12 kB · 2m10s");
  assert.equal(liveActivityText(running({ stage: "execute", stage_state: "enter", active_tool: "read_file" })), "running read_file · 0s");
  assert.equal(liveActivityText(running({ stage: "execute", stage_state: "enter" })), "running tool — target unknown · 0s");
});

test("live activity names the known wait after an exited stage without retaining that stage", () => {
  assert.equal(liveActivityText(running({ stage: "assemble", stage_state: "exit" })), "waiting for model · 0s");
  assert.equal(liveActivityText(running({ stage: "call_model", stage_state: "exit" })), "waiting for response parsing · 0s");
  assert.equal(liveActivityText(running({ stage: "parse", stage_state: "exit" })), "waiting for next action · 0s");
  assert.equal(liveActivityText(running({ stage: "dispatch", stage_state: "exit" })), "waiting for tool execution · 0s");
  assert.equal(liveActivityText(running({ stage: "execute", stage_state: "exit" })), "waiting for result recording · 0s");
  assert.equal(liveActivityText(running({ stage: "append", stage_state: "exit" })), "recording result · 0s");
  assert.equal(liveActivityText(running({ stage: "compact", stage_state: "exit" })), "waiting for next turn · 0s");
  assert.equal(liveActivityText(running({ stage: "wait_user", stage_state: "exit" })), "waiting to resume · 0s");
  assert.equal(liveActivityText(running({ stage: "future_stage", stage_state: "enter" })), "waiting — state unknown · 0s");
  assert.equal(liveActivityText(running({ stage: "future_stage", stage_state: "exit" })), "waiting — state unknown · 0s");
  assert.equal(liveActivityText({ run: { status: "idle" }, activity: {} }), "");
});

test("stream caret exists only beside unfinished prose that has started arriving", () => {
  assert.equal(showsStreamCaret({ type: "agent", text: "part", done: false }), true);
  assert.equal(showsStreamCaret({ type: "agent", reasoning: "thinking", done: false }), false);
  assert.equal(showsStreamCaret({ type: "agent", text: "done", done: true }), false);
  assert.equal(showsStreamCaret({ type: "tool", text: "output", done: false }), false);
});

test("every live status has elapsed time and never invents zero tokens 2qf", () => {
  const now = 66_000;
  assert.equal(liveActivityText(running({ started_at: 6_000, stage: "call_model", stage_state: "enter", progress: {}, stream: { started_at: 6_000 } }), now), "waiting for first token · 1m00s");
  assert.equal(liveActivityText(running({ started_at: 6_000, stage: "execute", stage_state: "enter", active_tool: "shell", tool_target: "dotnet test", tool_last_line: "Passed 41 tests" }), now), "running shell dotnet test · Passed 41 tests · 1m00s");
  assert.equal(liveActivityText(running({ started_at: 6_000, stage: "execute", stage_state: "enter", active_tool: "delegate", delegate: { status: "running", stage: "execute", tool: "read_file", target: "src/main.go", turn: 3 } }), now), "delegate — running read_file src/main.go · turn 3 · 1m00s");
  for (const activity of [
    { started_at: 1, stage: "assemble", stage_state: "enter" },
    { started_at: 1, stage: "call_model", stage_state: "enter", progress: {}, stream: {} },
    { started_at: 1, stage: "compact", stage_state: "enter" },
  ]) {
    const text = liveActivityText(running(activity), now);
    assert.ok(text.length > 0 && /\d+(?:m\d\d)?s$/.test(text), text);
    assert.doesNotMatch(text, /0 tokens/);
  }
});

test("an accelerated replay of long waits stays truthful at every sampled second 2qf", () => {
  const phases = [
    [0, 82, { stage: "call_model", stage_state: "enter", progress: {}, stream: { started_at: 1_000 } }],
    [83, 282, { stage: "execute", stage_state: "enter", active_tool: "shell", tool_target: "dotnet test", tool_last_line: "running tests" }],
    [283, 690, { stage: "execute", stage_state: "enter", active_tool: "delegate", delegate: { status: "running", stage: "call_model", turn: 4 } }],
    [691, 737, { stage: "wait_user", stage_state: "enter" }],
  ];
  for (const [first, last, phase] of phases) for (let second = first; second <= last; second++) {
    const text = liveActivityText(running({ ...phase, started_at: 1_000 }), 1_000 + second * 1000);
    assert.ok(text && /\d+(?:m\d\d)?s$/.test(text), `${second}: ${text}`);
    assert.doesNotMatch(text, /0 tokens/);
  }
});
