import assert from "node:assert/strict";
import test from "node:test";

import { runTimeSentence, runTimeHover, runTimeBuckets } from "./run-summary.js";

// Item 2ji (b): the operator rejected a dense line. "that's not very human
// readable." These are the words the item itself specifies.
test("2ji: the stop line reads as a sentence, largest share first", () => {
  const { text } = runTimeSentence({
    time: {
      total_ms: 130_000,
      model_ms: 100_000,
      prompt_ms: 20_000,
      generation_ms: 80_000,
      tool_ms: 18_000,
      waiting_ms: 12_000,
      compaction_ms: 0,
      unaccounted_ms: 0,
    },
    retries: 1,
  });
  assert.equal(
    text,
    "Took 2 min 10 s — the model 1 min 40 s (mostly writing), tools 18 s, waiting on you 12 s; one retry.",
  );
});

test("2ji: a run under ten seconds says only how long it took", () => {
  assert.equal(runTimeSentence({ time: { total_ms: 6_000, model_ms: 5_800 } }).text, "Took 6 s.");
  // Even then, something that went wrong is worth a word.
  assert.equal(
    runTimeSentence({ time: { total_ms: 6_000, model_ms: 5_800 }, empty_replies: 1 }).text,
    "Took 6 s. One empty reply.",
  );
});

test("2ji: nothing under five per cent is mentioned", () => {
  const { text } = runTimeSentence({
    time: { total_ms: 100_000, model_ms: 95_000, tool_ms: 1_000, waiting_ms: 4_000 },
  });
  assert.equal(text, "Took 1 min 40 s — the model 1 min 35 s.");
  assert.ok(!text.includes("tools"), "a one per cent bucket is noise");
  assert.ok(!text.includes("waiting"), "a four per cent bucket is noise");
});

test("2ji: the model's aside is only claimed when the server reported the split", () => {
  const reported = runTimeSentence({
    time: { total_ms: 100_000, model_ms: 90_000, prompt_ms: 70_000, generation_ms: 20_000 },
  });
  assert.match(reported.text, /mostly reading the conversation/);
  // No timings from the server means no claim about where the model's time went.
  const silent = runTimeSentence({ time: { total_ms: 100_000, model_ms: 90_000 } });
  assert.equal(silent.text, "Took 1 min 40 s — the model 1 min 30 s.");
  assert.ok(!silent.text.includes("mostly"), "an unreported split must not be guessed at");
});

// The narrowing, on the surface the operator reads: a bucket the server could not
// supply says so, and is never shown as a zero.
test("2ji: an unreported bucket reads 'not reported' in the hover", () => {
  const hover = runTimeHover({ time: { total_ms: 12_000, model_ms: 9_000, tool_ms: 0 } });
  assert.match(hover, /^total 12 s$/m);
  assert.match(hover, /^prompt processing: not reported$/m);
  assert.match(hover, /^generating: not reported$/m);
  assert.ok(!/prompt processing: 0/.test(hover), "absent must never render as zero");
  // A bucket that IS reported as zero is a measured zero and shows as one.
  assert.match(hover, /^tools \(sum, they overlap\): 0 ms$/m);
});

test("2ji: a run journalled before this item existed says nothing at all", () => {
  assert.deepEqual(runTimeSentence({ reason: "done" }), { text: "", title: "" });
  assert.deepEqual(runTimeSentence({ time: { total_ms: 0 } }), { text: "", title: "" });
  assert.deepEqual(runTimeSentence(null), { text: "", title: "" });
  assert.deepEqual(runTimeBuckets(null), []);
});

test("2ji: an hour-long run reads in hours and minutes", () => {
  const { text } = runTimeSentence({
    time: { total_ms: 4_200_000, model_ms: 2_100_000, compaction_ms: 1_800_000, unaccounted_ms: 300_000 },
    compactions: 11,
  });
  // known-good-live is a real run of this shape: most of its wall clock went on
  // summarising the conversation, which is exactly the thing nobody could see.
  assert.match(text, /^Took 1 h 10 min — /);
  assert.match(text, /the model 35 min, summarising the conversation 30 min/);
  assert.match(text, /; 11 summaries\.$/);
});
