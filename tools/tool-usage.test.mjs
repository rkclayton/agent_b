import assert from "node:assert/strict";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { readChats, tables, usage } from "./tool-usage.mjs";

const line = (seq, type, data = {}, run = "r1") => JSON.stringify({ seq, ts: `2026-10-0${1 + (seq % 3)}T00:00:00Z`, session_id: "stand-in", run_id: run, type, data });
const offered = ["read_file", "recall", "chat_history", "remember"];

test("the reader counts each tool, its context and the compaction follow-up 2wc", async () => {
  const folder = await mkdtemp(join(tmpdir(), "agentb-tool-usage-"));
  await writeFile(join(folder, "s1.jsonl"), [
    line(1, "run.started"), line(2, "model.request", { tools_offered: offered }), line(3, "model.response"),
    line(4, "tool.call", { name: "remember", turn: 1 }), line(5, "tool.result", { name: "remember" }),
    line(6, "compaction", { kind: "fresh" }), line(7, "model.response"), line(8, "tool.call", { name: "chat_history", turn: 2 }),
    line(8, "tool.call", { name: "chat_history", turn: 2 }), line(9, "run.stopped"),
  ].join("\n") + "\n");
  await writeFile(join(folder, "s2.jsonl"), [
    line(1, "model.request", { tools_offered: ["read_file", "remember"] }, "r2"), line(2, "tool.call", { name: "read_file", turn: 1 }, "r2"),
    line(3, "tool.result", { name: "read_file" }, "r2"), line(4, "tool.call", { name: "recall", turn: 1 }, "r2"),
    line(5, "compaction", { kind: "fresh" }, "r2"), line(6, "tool.call", { name: "remember", turn: 2 }, "r2"),
    line(7, "compaction", { kind: "fresh" }, "r2"), "not json",
  ].join("\n") + "\n");
  await writeFile(join(folder, "notes.txt"), "ignored");
  const result = usage(readChats(folder));
  assert.equal(result.chats, 2);
  assert.equal(result.runs, 2);
  assert.deepEqual(Object.fromEntries(Object.entries(result.totals).map(([name, one]) => [name, [one.calls, one.chats.size, one.runs.size]])), { recall: [1, 1, 1], chat_history: [1, 1, 1], remember: [2, 2, 2] });
  assert.deepEqual(Object.fromEntries(Object.entries(result.offered).map(([name, runs]) => [name, runs.size])), { recall: 1, chat_history: 1, remember: 2 });
  assert.deepEqual(result.calls.map(({ tool, before, compactedEarlier }) => [tool, before, compactedEarlier]), [["chat_history", "model.response", true], ["recall", "tool.result read_file", false]]);
  assert.deepEqual([result.compactions, result.followed], [3, 1]);
  const printed = tables(result);
  assert.match(printed, /\| chat_history \| 1 \| 1 \| 1 \| 2026-10-0\d \| 2026-10-0\d \| 1 \|/);
  assert.match(printed, /\| 3 \| 1 \| 33% \|/);
  assert.doesNotMatch(printed, /stand-in|s1|s2|r1|r2|agentb-tool-usage/);
});
