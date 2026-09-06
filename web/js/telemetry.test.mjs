import assert from "node:assert/strict";
import test from "node:test";

import { liveTelemetry, recordedTelemetry } from "./telemetry.js";

test("live telemetry projects chunk age, reasoning, and a recent rate", () => {
  const session = { activity: { stream: { last_chunk_at: 3000, started_at: 1000, reasoning_chars: 9, has_chunk: true, done: false, rate: 2 } } };
  assert.deepEqual(liveTelemetry(session, 4000), { ageSeconds: 1, reasoningTokens: 3, rate: 2, hasChunk: true });
});

test("live telemetry counts Unicode characters rather than UTF-16 units", () => {
  const session = { activity: { stream: { last_chunk_at: 2000, started_at: 1000, reasoning_chars: 4, has_chunk: true, done: false, rate: 1 } } };
  assert.equal(liveTelemetry(session, 3000).reasoningTokens, 2);
});

test("snapshot directly carries active stream telemetry", () => {
  const session = { activity: { stream: { last_chunk_at: 2000, started_at: 1000, reasoning_chars: 8, has_chunk: true, done: false, rate: 2 } } };
  assert.equal(session.activity.stream.reasoning_chars, 8);
  assert.equal(session.activity.stream.done, false);
});

test("replay telemetry is explicitly final recorded data", () => {
  const session = { timeline: [{
    type: "model.response",
    data: { reasoning_tokens: 1847, reasoning_tokens_estimated: true, timings: { predicted_per_second: 23.6 } },
  }] };
  assert.deepEqual(recordedTelemetry(session), { reasoningTokens: 1847, reasoningTokensEstimated: true, rate: 24 });
});
