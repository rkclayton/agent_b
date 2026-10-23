import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { copyFile, mkdir, mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { basename, dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";
import { startStubModel, stubModel } from "./stub-model.mjs";

const here = resolve(fileURLToPath(new URL("..", import.meta.url)));
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));
const fixedKeys = new Set(["system", "project", "skills", "workspace_memory", "agent_memory", "tools"]);
const proofSummaryPct = .9;
const proofTask = "Read every numbered text file in this folder in filename order. After each read, append that file's first line to notes.md. Finish by saying how many numbered files there were.";
const proofFiller = "fixture words preserve a long real-model context without carrying private data. ".repeat(90);
const proofStep = `${proofTask}\n\n${proofFiller}`;
const proofDetail = "47113";
export const startupWaitMs = Math.ceil(5357 * 1.5);
// A step's allowance is three times the longest step completed in this invocation, between
// 600 and 1,800 seconds (planner, 2026-10-23 14:05); before any step completes it is the 1,800-second cap.
export const stepAllowance = (longestMs = 0) => longestMs > 0 ? Math.min(1800000, Math.max(600000, Math.ceil(longestMs * 3))) : 1800000;
// Against the stand-in a step answers in under a second; one that takes a minute is a harness fault.
export const stubStepMs = 60000;
export const notRun = (name, reason) => ({ run: name, not_run: reason, conditions: [{ condition: 0, pass: false, turn: 0, message: reason }] });
export function drivenStepPlan(ceiling, fixedPrompt, minimum) {
  const tokens = Math.ceil([...proofStep].length * 10 / 36);
  const line = Math.ceil(fixedPrompt + proofSummaryPct * (ceiling - fixedPrompt));
  const firstCrossing = Math.ceil((line - fixedPrompt) / tokens);
  return { tokens, firstCrossing, maximum: firstCrossing * minimum + 2 };
}
export function measuredStepMaximum(plan, minimum, beforeChangeable, afterChangeable) {
  return plan.firstCrossing + Math.ceil((beforeChangeable - afterChangeable) / plan.tokens) * minimum + 2;
}

export function startupFailure(wait, last) { return `candidate page did not answer after ${(wait / 1000).toFixed(3)} seconds; last log: ${last || "none"}`; }
export function proofSelection(machine, profile) {
  const original = profile.agents.find((one) => one.name === "agent_b") || profile.agents[0];
  const connection = machine.connections.find((one) => one.id === original.b);
  assert.ok(connection, "installed agent connection is unavailable");
  const model = connection.models.find((one) => one.model === connection.model) || connection.models[0];
  assert.ok(model, "installed model record is unavailable");
  const window = Number(model.context?.n_ctx || 0); assert.ok(window > 0, "installed model window is unavailable");
  return { agent: { ...original, c: "", d: "" }, connection, model, record: { connection: "active profile agent_b role b", model: model.model, window } };
}

export function proofPlan(machine, profile) {
  const selected = proofSelection(machine, profile), window = selected.record.window;
  const sized = (minimum) => Math.ceil(40 * window / 32768) * minimum;
  // 2wi: `stub` cases gate a release against the stand-in model; `real` cases are the scheduled
  // measurement; needs-recall is shelved (operator, 2026-10-23 15:19) and runs only when named.
  const runs = [
    { name: "fresh", minimum: 3, stub: true, real: true },
    { name: "typed-continue", minimum: 3, interrupt: true, continueText: "continue", stub: true, real: true },
    { name: "Continue", minimum: 3, interrupt: true, continueText: "Continue", stub: true, real: true },
    { name: "queued-message", minimum: 3, queue: true, stub: true, real: true },
    { name: "full", minimum: 1, real: true },
    { name: "needs-recall", minimum: 1, plant: proofDetail }
  ].map((run) => ({ ...run, window, files: sized(run.minimum) }));
  // 2we: a prune and a summary in one crossing needs older results over half the context
  // and the rest still over the line, which only a lowered line produces: half the room in
  // results and a quarter in one user message under 0.9, then the line moves to 0.25.
  const room = Math.max(0, window - Number(selected.model.context?.reserve_output || 0) - 3000), unit = Math.ceil([...proofFiller].length * 10 / 36);
  runs.push({ name: "prune+summary", minimum: 1, window, lowered: .25, files: Math.max(2, Math.floor(.5 * room / unit)), history: Math.max(1, Math.ceil(.27 * room / unit)), stub: true, real: true });
  return { window, runs };
}

// The cases one invocation runs: the named ones, else the mode's own set.
export function proofCases(plan, { real = false, only = "" } = {}) {
  const named = new Set(String(only || "").split(",").filter(Boolean));
  return plan.runs.filter((run) => named.size ? named.has(run.name) : real ? run.real : run.stub);
}

// The stand-in connection: the installed one's agent and settings, pointed at the stub, with a
// fixed 32,768-token window, no credential and no server-side counting, so nothing leaves the PC.
export const stubWindow = 32768;
export function stubConnection(machine, profile, url = "") {
  const selected = proofSelection(machine, profile), connection = structuredClone(selected.connection);
  delete connection.credential; connection.base_url = url; connection.protocol = "chat-completions"; connection.label = "stand-in model"; connection.model = stubModel;
  connection.models = [{ ...structuredClone(selected.model), model: stubModel, context: { n_ctx: stubWindow, reserve_output: 8192 }, reasoning: { ...selected.model.reasoning, enabled: false },
    capabilities: { server: stubModel, n_ctx: stubWindow, tokenize: false, apply_template: false, streaming: true, tool_calls: true, probed_at: new Date().toISOString(), findings: [] } }];
  return { ...machine, connections: machine.connections.map((one) => one.id === connection.id ? connection : one) };
}

async function filesBelow(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await filesBelow(path));
    else result.push(path);
  }
  return result;
}

export async function retainJournals(sourceRoot, evidenceRoot, run, redactions = []) {
  const destination = join(evidenceRoot, run); await mkdir(destination, { recursive: true });
  for (const file of (await filesBelow(sourceRoot)).filter((one) => one.endsWith(".jsonl"))) {
    const target = join(destination, relative(sourceRoot, file)); await mkdir(dirname(target), { recursive: true });
    let content = await readFile(file, "utf8"); for (const value of redactions.filter(Boolean)) content = content.replaceAll(value, "<connection>");
    await writeFile(target, content);
  }
}

async function lastCandidateLog(data, fallback = "") {
  let last = fallback;
  for (const file of await filesBelow(join(data, "logs")).catch(() => [])) {
    const line = (await readFile(file, "utf8")).trim().split(/\r?\n/).filter(Boolean).at(-1); if (!line) continue;
    try { const event = JSON.parse(line); last = [event.type, event.data?.stage, event.data?.state, event.data?.reason].filter(Boolean).join(" "); } catch {}
  }
  return last;
}

export async function sourceHash(root = here) {
  const context = join(root, "internal", "context"), hash = createHash("sha256");
  for (const file of (await filesBelow(context)).filter((one) => one.endsWith(".go") && !one.endsWith("_test.go")).sort()) {
    hash.update(relative(context, file).replaceAll("\\", "/")).update("\0").update(await readFile(file)).update("\0");
  }
  return hash.digest("hex");
}

const fixed = (budget) => Object.entries(budget?.categories || {}).filter(([name]) => fixedKeys.has(name)).reduce((sum, [, value]) => sum + Number(value || 0), 0);
export function runEnding(events, error) {
  const budgets = events.filter((event) => event.type === "budget" && event.data?.used_measured > 0);
  const repeated = budgets.length > 1 && budgets.at(-1).data.used_measured === budgets.at(-2).data.used_measured;
  if (budgets.length && !repeated) return "";
  if (error && /timed out.*allowance/.test(error.message)) return `expired allowance: ${error.message}`;
  const stopped = events.filter((event) => event.type === "run.stopped").at(-1);
  return stopped ? `terminal event: run.stopped ${stopped.data?.reason || "without a reason"}` : `last event: ${events.at(-1)?.type || "none"}`;
}

// The message list the request after a compaction carries, rebuilt from the journal as the
// projection applies it: appended in order, a hand-off at the front, removals and a summarizing
// compaction's affected ids taken out. Never the newest-50 screen /api/state returns.
export function journalMessages(events, compact) {
  if (!events.some((one) => one.type === "message.appended")) return undefined;
  const end = events.find((one) => one.seq > compact.seq && (one.type === "model.request" || one.type === "model.response"))?.seq ?? Infinity;
  let messages = [];
  for (const event of events.filter((one) => one.seq < end)) {
    const message = event.data?.message;
    if (event.type === "message.appended" && message) {
      const at = message.category !== "summary" ? messages.length : /^(Fresh-context hand-off:\n|Compacted: )/.test(String(message.content || "")) ? 0 : Math.min(1, messages.length);
      messages.splice(at, 0, { ...message });
    } else if (event.type === "message.updated" && event.data?.patch && "content" in event.data.patch) {
      for (const one of messages) if (one.id === event.data.id) one.content = event.data.patch.content;
    } else if (event.type === "message.removed") messages = messages.filter((one) => one.id !== event.data?.id);
    else if (event.type === "compaction" && ["summarize", "fresh", "prune+fresh"].includes(event.data?.kind)) { const affected = new Set(event.data.affected_ids || []); messages = messages.filter((one) => !affected.has(one.id)); }
    else if (event.type === "compaction" && event.data?.kind === "prune") { const affected = new Set(event.data.affected_ids || []); for (const one of messages) if (affected.has(one.id)) one.content = `[${one.name || "tool"} result removed at compaction, ${Number(one.tokens || 0)} tokens; recall reads it]`; }
  }
  return messages;
}

// 2wd (f): the turn that asks for the planted detail — its request, whether it called
// recall, whether the reply carries the detail, and whether the hand-off or the context did.
export function recallAnswer(events, marker, detail) {
  const after = events.filter((one) => one.seq > marker), last = events.filter((one) => one.type === "compaction" && one.seq < marker).at(-1);
  const reply = after.filter((one) => one.type === "model.response").at(-1), handoffs = new Set(events.filter((one) => one.type === "compaction").map((one) => one.data?.summary_message_id).filter(Boolean));
  const recall = after.some((one) => one.type === "tool.call" && one.data?.name === "recall"), correct = String(reply?.data?.content || "").includes(detail);
  const handoff = events.some((one) => one.type === "message.appended" && handoffs.has(one.data?.message?.id) && String(one.data.message.content || "").includes(detail));
  const in_context = Boolean(last && (journalMessages(events, last) || []).some((one) => String(one.content || "").includes(detail)));
  return { request: after.find((one) => one.type === "model.request")?.seq, recall, correct, handoff, in_context, pass: recall || correct };
}

export function assessRun(name, events, task, notes, fileCount, minimumCompactions, output = false, ending = "", driven = false, { lowered = null, ask = null } = {}) {
  const stopped = events.filter((event) => event.type === "run.stopped").at(-1);
  const compactions = events.filter((event) => event.type === "compaction");
  const pct = (seq) => lowered && seq > lowered.seq ? lowered.pct : proofSummaryPct;
  const lines = notes.trimEnd().split(/\r?\n/);
  // 2wh (d): a run's closing total — a budget the run stops after with no request — is not a turn.
  const measured = events.filter((event) => event.type === "budget" && event.data?.used_est > 0);
  // 2wi: nor is a budget published between runs — after a run stopped and before the next started.
  const between = (one) => events.filter((event) => event.seq < one.seq && (event.type === "run.started" || event.type === "run.stopped")).at(-1)?.type === "run.stopped";
  const budgets = measured.filter((one) => !between(one) && events.find((event) => event.seq > one.seq && (event.type === "model.request" || event.type === "run.stopped"))?.type !== "run.stopped");
  const rows = budgets.map((before, index) => {
    const end = budgets[index + 1]?.seq ?? Infinity, ran = compactions.filter((one) => one.seq > before.seq && one.seq < end);
    const response = ran.length ? events.find((one) => one.seq > ran.at(-1).seq && one.type === "model.response" && Number(one.data?.usage?.prompt_tokens) > 0) : undefined;
    const after = response ? measured.find((one) => one.seq > response.seq) : undefined;
    const prompt = Number(before.data.used_est), line = Math.ceil(fixed(before.data) + pct(before.seq) * (Number(before.data.ceiling) - fixed(before.data)));
    const recall = events.some((one) => one.type === "tool.call" && one.data?.name === "recall" && one.seq > before.seq && one.seq < end);
    const beforeUsed = Number(before.data.used_measured), afterUsed = Number(response?.data?.usage?.prompt_tokens);
    const beforeChangeable = beforeUsed - fixed(before.data), afterChangeable = afterUsed - fixed(after?.data || before.data);
    return { turn: index + 1, before, after, response, ran, prompt, line, recall, crossed: prompt >= line, beforeChangeable, afterChangeable, cut: response ? (beforeChangeable - afterChangeable) / beforeChangeable : NaN };
  });
  const around = (compact) => rows.find((one) => one.ran.includes(compact));
  const misplaced = rows.find((one) => one.crossed !== Boolean(one.ran.length));
  const weak = rows.filter((one) => one.ran.length).find((one) => !one.response || !Number.isFinite(one.cut) || one.afterChangeable * 2 >= one.beforeChangeable);
  const expected = Array.from({ length: fileCount }, (_, index) => String(index + 1).padStart(4, "0"));
  const taskLocations = compactions.map((compact) => {
    const messages = journalMessages(events, compact) ?? events.find((event) => event.seq > compact.seq && event.type === "model.request")?.body?.messages ?? [], position = messages.findIndex((message) => String(message.content || "").includes(task)), message = messages[position];
    return { compact: compact.seq, role: message?.role, id: message?.id, position: position + 1, affected: Boolean(message && compact.data?.affected_ids?.includes(message.id)), ids: messages.map((one) => one.id) };
  });
  const missingTask = taskLocations.find((one) => one.position < 1 || one.affected);
  const replies = compactions.map((compact) => {
    const messages = journalMessages(events, compact) ?? events.find((event) => event.seq > compact.seq && event.type === "model.request")?.body?.messages ?? [], response = events.find((event) => event.seq > compact.seq && event.type === "model.response"), handoff = messages.find((one) => one.id === compact.data?.summary_message_id);
    const calls = new Set(messages.flatMap((one) => one.tool_calls || []).map((one) => one.id));
    const malformed = !messages.length || messages.some((one) => one.role === "tool" && !calls.has(one.tool_call_id)) || messages.some((one) => !String(one.content || "").trim() && !String(one.reasoning || "").trim() && !(one.tool_calls || []).length);
    const empty = !String(response?.data?.content || "").trim(), tool_calls = (response?.data?.tool_calls || []).length, reasoning = Number(response?.data?.reasoning_tokens || 0) > 0;
    return { compact: compact.seq, turn: around(compact)?.turn || 0, response: response?.seq, finish_reason: response?.data?.finish_reason || "", reasoning_tokens: Number(response?.data?.reasoning_tokens || 0), completion_tokens: Number(response?.data?.usage?.completion_tokens || 0), empty, tool_calls, answered: !empty || tool_calls > 0, reasoning, message_count: messages.length, handoff_tokens: Number(handoff?.tokens || 0), handoff_first_80: String(handoff?.content || "").slice(0, 80), reading: malformed ? "HARNESS" : "MODEL" };
  });
  const emptyStop = ["reply_empty", "reply_empty_reasoning_shown"].includes(stopped?.data?.reason);
  const badReply = replies.find((one) => !one.answered);
  const noteIndex = expected.findIndex((line, index) => lines[index] !== line), noteTurn = Math.max(1, noteIndex + 1);
  const condition1Pass = compactions.length >= minimumCompactions && !misplaced;
  const conditions = [
    { condition: 1, pass: condition1Pass, turn: misplaced?.turn || rows.at(-1)?.turn || 0, message: misplaced ? `turn ${misplaced.turn} ${misplaced.crossed ? `crossed line ${misplaced.line} with prompt ${misplaced.prompt} but saw 0 compactions` : `compacted below line ${misplaced.line} with prompt ${misplaced.prompt}`}` : `turn ${rows.at(-1)?.turn || 0} saw ${compactions.length} compactions; required at least ${minimumCompactions}` },
    { condition: 2, pass: !weak, turn: weak?.turn || 0, message: weak ? (weak.response ? `turn ${weak.turn} compaction changed changeable prompt by ${(weak.cut * 100).toFixed(1)}% (${weak.beforeChangeable} to ${weak.afterChangeable})` : `turn ${weak.turn} compaction seq ${weak.ran.at(-1)?.seq} saw no following model.response prompt count`) : "all compactions removed more than half" },
    { condition: 3, pass: stopped?.data?.reason === "done", turn: rows.at(-1)?.turn || 0, message: `turn ${rows.at(-1)?.turn || 0} run ended ${stopped?.data?.reason || "without a stop"}` },
    driven
      ? { condition: 4, pass: !badReply && !emptyStop, turn: badReply?.turn || rows.at(-1)?.turn || 0, message: badReply ? `turn ${badReply.turn} post-compaction response ${badReply.response ?? "missing"} had neither prose nor a tool call` : emptyStop ? `run ended ${stopped.data.reason}` : "every post-compaction response answered with prose or a tool call" }
      : { condition: 4, pass: JSON.stringify(lines) === JSON.stringify(expected), turn: noteTurn, message: `turn ${noteTurn} notes.md saw ${JSON.stringify(lines[noteIndex] ?? "<missing>")}; expected ${JSON.stringify(expected[noteIndex] ?? "<end>")}` },
    { condition: 5, pass: !missingTask, turn: missingTask ? around(compactions.find((one) => one.seq === missingTask.compact))?.turn || 0 : 0, message: missingTask ? `task ${missingTask.id ? "affected" : "absent"} after compaction seq ${missingTask.compact}; ids=${missingTask.ids.join(",")}` : "task retained after every compaction" }
  ];
  const answer = ask ? recallAnswer(events, ask.marker, ask.detail) : null;
  if (answer) conditions.push({ condition: 6, pass: answer.pass, turn: rows.at(-1)?.turn || 0, message: `request ${answer.request ?? "missing"} asked for the planted detail: recall=${answer.recall} reply=${answer.correct}` });
  if (output) {
    process.stdout.write(`\nCOMPACTION TRACE: ${name}\n| turn | assembly prompt | line | crossed | compaction | count after | fixed before | fixed after | share cut | recall |\n| ---: | ---: | ---: | :---: | :---: | ---: | ---: | ---: | ---: | :---: |\n`);
    for (const row of rows) process.stdout.write(`| ${row.turn} | ${row.prompt} | ${row.line} | ${row.crossed ? "yes" : "no"} | ${row.ran.length ? `yes (${row.ran.map((one) => one.data?.kind || "?").join(", ")})` : "no"} | ${row.response?.data?.usage?.prompt_tokens ?? "—"} | ${fixed(row.before.data)} | ${row.response ? fixed(row.after?.data || row.before.data) : "—"} | ${Number.isFinite(row.cut) ? (row.cut * 100).toFixed(1) + "%" : "—"} | ${row.recall ? "yes" : "no"} |\n`);
    if (answer) process.stdout.write(`\nNEEDS RECALL: request seq ${answer.request ?? "missing"}; recall called=${answer.recall}; reply carries the detail=${answer.correct}; hand-off carried it=${answer.handoff}; still in context=${answer.in_context}\n`);
    if (ending) process.stdout.write(`\nPROOF END: ${ending}\n`);
    for (const location of taskLocations) process.stdout.write(`\nTASK RETENTION: compaction seq ${location.compact}; ${location.id ? `message ${location.position} role=${location.role} id=${location.id} affected=${location.affected} quote=${JSON.stringify(task)}` : `absent ids=${location.ids.join(",")}`}\n`);
    for (const reply of replies) process.stdout.write(`EMPTY REPLY READING: compaction seq ${reply.compact}; response=${reply.response ?? "none"}; finish=${reply.finish_reason}; reasoning_tokens=${reply.reasoning_tokens}; completion_tokens=${reply.completion_tokens}; empty=${reply.empty}; tool_calls=${reply.tool_calls}; reasoning=${reply.reasoning}; messages=${reply.message_count}; handoff_tokens=${reply.handoff_tokens}; handoff_first_80=${JSON.stringify(reply.handoff_first_80)}; ${reply.reading}\n`);
    process.stdout.write(`\n| condition | pass | deciding turn | message |\n| ---: | :---: | ---: | --- |\n${conditions.map((one) => `| ${one.condition} | ${one.pass ? "yes" : "no"} | ${one.turn} | ${one.pass ? "—" : one.message} |`).join("\n")}\n`);
  }
  const measurements = compactions.map((compact) => { const row = around(compact); return { before: row?.before?.data?.used_measured, fixed_prompt_before: row && fixed(row.before.data), after: row?.response?.data?.usage?.prompt_tokens, fixed_prompt_after: row?.response && fixed(row.after?.data || row.before.data) }; });
  const followed = compactions.filter((compact) => events.some((one) => one.type === "tool.call" && one.data?.name === "recall" && one.seq > compact.seq && one.run_id === compact.run_id)).length;
  return { result: { run: name, compactions: measurements, excluded_budget_rows: measured.length - budgets.length, kinds: compactions.map((one) => one.data?.kind || ""), compactions_followed_by_recall: followed, recall_calls: events.filter((one) => one.type === "tool.call" && one.data?.name === "recall").length, ...(answer ? { needs_recall: answer } : {}), ended: stopped?.data?.reason || "without a stop", notes_lines: lines.filter(Boolean).length }, conditions, rows, taskLocations, replies };
}
export function verifyRun(name, events, task, notes, fileCount, minimumCompactions, driven = false) {
  const checked = assessRun(name, events, task, notes, fileCount, minimumCompactions, false, "", driven), failure = checked.conditions.find((one) => !one.pass);
  assert.ok(!failure, `${name}: ${failure?.message}`); return checked.result;
}

async function freePort() {
  const server = createServer();
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const port = server.address().port;
  await new Promise((done) => server.close(done));
  return port;
}

async function json(url, options = {}) {
  const response = await fetch(url, { signal: AbortSignal.timeout(60000), ...options }), text = await response.text();
  if (!response.ok) throw new Error(`proof request ${response.status}`);
  return text ? JSON.parse(text) : {};
}

export function uniqueEvents(events) {
  const seen = new Set();
  return events.filter((event) => { const key = `${event.session_id || ""}\0${event.seq}\0${event.type}`; if (seen.has(key)) return false; seen.add(key); return true; });
}

async function eventsIn(data, sessionID) {
  const files = (await filesBelow(data)).filter((file) => file.endsWith(".jsonl"));
  const values = [];
  for (const file of files) for (const line of (await readFile(file, "utf8").catch((error) => error.code === "ENOENT" ? "" : Promise.reject(error))).split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line); if (event.session_id === sessionID) values.push(event);
  }
  return uniqueEvents(values.sort((a, b) => a.seq - b.seq));
}

async function waitFor(check, label, timeout) {
  for (const end = Date.now() + timeout; Date.now() < end;) { const value = await check(); if (value) return value; await sleep(250); }
  throw new Error(`${label} timed out`);
}

// waitForStep waits for one step, or for units steps when a wait covers several, against the
// step allowance times units; a step over it is not run, and the proof moves to the next case.
// Only a wait that counts — one driven user message from its send to its final reply — sets
// the longest step; a partial wait (results in, a stop landed) never does.
export async function waitForStep(read, done, { label = "", step = 1, units = 1, allowanceMs, timing, counts = false }) {
  const started = Date.now(), allowance = allowanceMs * units;
  for (;;) {
    const values = await read();
    if (done(values)) {
      if (counts) timing.longestStepMs = Math.max(timing.longestStepMs || 0, Date.now() - started);
      if (label) process.stdout.write(`COMPACTION PROOF WAIT: ${label} took ${((Date.now() - started) / 1000).toFixed(3)} of ${(allowance / 1000).toFixed(3)} seconds
`);
      return values;
    }
    if (Date.now() - started >= allowance) throw Object.assign(new Error(`not run — step ${step} exceeded ${allowance / 1000} s`), { notRun: `not run — step ${step} exceeded ${allowance / 1000} s` });
    await sleep(Math.min(250, allowance));
  }
}

// 2wh: "this folder" is the chat's own folder, made when the chat is created, so each case
// plants its numbered files there after creating its chat, and reads notes.md from it.
export async function prepareCase(created, fileCount) {
  const folder = created?.session?.workspace;
  if (!folder) throw new Error("the new chat has no chat folder");
  for (let index = 1; index <= fileCount; index++) await writeFile(join(folder, `${String(index).padStart(4, "0")}.txt`), `${String(index).padStart(4, "0")}\n${proofFiller}\n`);
  return { folder, notes: join(folder, "notes.md") };
}

async function prepare(sourceData, root, window, stubURL = "") {
  let machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8"));
  const active = machine.profiles.active;
  const profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8"));
  if (stubURL) machine = stubConnection(machine, profile, stubURL);
  const selected = proofSelection(machine, profile), { agent, connection, model } = selected;
  if (stubURL) selected.record.connection = "stand-in model";
  const declaredWindow = window || selected.record.window; model.context.n_ctx = declaredWindow;
  machine.connections = [connection]; machine.listen = `127.0.0.1:${await freePort()}`;
  machine.workspace = join(root, "workspace"); machine.log_dir = join(root, "data", "logs");
  machine.profiles = { active, names: [active] }; machine.context.summary_pct = .9;
  machine.run.max_turns = Math.max(machine.run.max_turns || 0, 600); machine.run.max_tool_calls = Math.max(machine.run.max_tool_calls || 0, 600);
  machine.approval.mode = "boundary-only"; machine.agents = [{ ...agent, toolset: ["read_file", "list_dir", "write_file", "edit_file", "recall"] }];
  profile.agents = [{ ...agent, toolset: ["read_file", "list_dir", "write_file", "edit_file", "recall"] }];
  await mkdir(join(root, "data", "profiles", active), { recursive: true }); await mkdir(machine.workspace, { recursive: true });
  await writeFile(join(root, "data", "harness.json"), JSON.stringify(machine));
  await writeFile(join(root, "data", "profiles", active, "profile.json"), JSON.stringify(profile));
  if (connection.credential) await copyFile(join(sourceData, `.agentb-connection-credential-${connection.credential}.dpapi`), join(root, "data", `.agentb-connection-credential-${connection.credential}.dpapi`));
  return { active, port: Number(machine.listen.split(":").at(-1)), task: proofTask, step: proofStep, workspace: machine.workspace, data: join(root, "data"), record: { ...selected.record, window: declaredWindow }, redactions: [connection.id, connection.label, connection.base_url, connection.credential].filter(Boolean) };
}

// The reads the stand-in makes after each user message: three for a driven step; every planted
// file then none for prune+summary's two messages. An interrupted or queued first message keeps
// reading, a tenth of a second a request, so the stop or the second message lands mid-run.
export const stubOptions = (spec) => spec.lowered ? { calls: [spec.files, 0] } : spec.interrupt ? { calls: [spec.files, 3], delayMs: 100 } : spec.queue ? { calls: [12, 3], delayMs: 100 } : { calls: [3] };

async function runGroup(app, sourceData, window, specs, startOnly = false, timing = {}, evidenceRoot = "", stub = false) {
  const root = await mkdtemp(join(tmpdir(), "agentb-compaction-proof-"));
  let child, eventsAbort, model;
  try {
    if (stub) model = await startStubModel(stubOptions(specs[0]));
    const prepared = await prepare(sourceData, root, window, model?.url);
    let lastLine = "";
    const startupAt = Date.now();
    child = spawn(app, ["--config", join(prepared.data, "harness.json"), "--app-root", here, "--data-root", prepared.data], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
    child.stderr.on("data", (part) => { lastLine = String(part).trim().split(/\r?\n/).filter(Boolean).at(-1)?.replaceAll(root, "<isolated root>") || lastLine; for (const value of prepared.redactions) lastLine = lastLine.replaceAll(value, "<connection>"); });
    const base = `http://127.0.0.1:${prepared.port}`;
    let document;
    const startAllowance = timing.slowestStartupMs ? Math.ceil(timing.slowestStartupMs * 3) : startupWaitMs;
    const attempt = () => waitFor(async () => { lastLine = await lastCandidateLog(prepared.data, lastLine); if (child.exitCode !== null) throw new Error(startupFailure(startAllowance, lastLine)); return fetch(`${base}/chat`).catch(() => null); }, "candidate page", startAllowance);
    let firstFailure = "";
    try { document = await attempt(); }
    catch (error) {
      firstFailure = `${String(error.message) === "candidate page timed out" ? startupFailure(startAllowance, await lastCandidateLog(prepared.data, lastLine)) : error.message}`;
      process.stdout.write(`COMPACTION PROOF START RETRY: ${specs[0].name} attempt 1 used ${(startAllowance / 1000).toFixed(3)} seconds; ${firstFailure}\n`);
      try { document = await attempt(); }
      catch (retryError) { const second = String(retryError.message) === "candidate page timed out" ? startupFailure(startAllowance, await lastCandidateLog(prepared.data, lastLine)) : retryError.message; throw new Error(`${specs[0].name} start attempt 1: ${firstFailure}; attempt 2: ${second}`); }
    }
    const startupMs = Date.now() - startupAt;
    if (timing.slowestStartupMs) timing.slowestStartupMs = Math.max(timing.slowestStartupMs, startupMs);
    process.stdout.write(`COMPACTION PROOF: candidate page answered in ${(startupMs / 1000).toFixed(3)} seconds within ${(startAllowance / 1000).toFixed(3)}-second allowance\n`);
    const html = await document.text(), token = html.match(/agentb-mutation-token" content="([^"]+)/)?.[1], cookie = document.headers.get("set-cookie")?.split(";")[0] || "";
    assert.ok(token, "model unreachable"); const headers = { "Content-Type": "application/json", "X-AgentB-Mutation-Token": token, Cookie: cookie };
    eventsAbort = new AbortController();
    const eventsResponse = await fetch(`${base}/api/events`, { headers, signal: eventsAbort.signal });
    assert.ok(eventsResponse.ok, "candidate event stream is unavailable");
    void eventsResponse.body?.pipeTo(new WritableStream()).catch(() => {});
    if (startOnly) { const state = await json(`${base}/api/state?session=proof-example`, { headers }); return { record: prepared.record, startupMs, stateExample: { sessions: Object.keys(state.sessions || {}).length } }; }
    const wait = (done, label, step = 1, units = 1, counts = false) => waitForStep(read, done, { label, step, units, counts, allowanceMs: stub ? stubStepMs : stepAllowance(timing.longestStepMs), timing });
    let read;
    const rows = [];
    for (const spec of specs) {
      const created = await json(`${base}/api/sessions`, { method: "POST", headers, body: JSON.stringify({ agent_id: "agent_b" }) });
      const planted = await prepareCase(created, spec.files);
      const id = created.session.id, send = (text) => json(`${base}/api/message`, { method: "POST", headers, body: JSON.stringify({ session_id: id, text }) });
      let runError, lowered = null, ask = null;
      read = () => eventsIn(prepared.data, id); const last = async () => (await eventsIn(prepared.data, id)).at(-1)?.seq || 0;
      const completedAfter = (after) => (events) => { const reply = events.find((one) => one.seq > after && one.type === "model.response"); return reply && events.some((one) => one.seq > reply.seq && one.type === "run.stopped"); };
      try {
        if (spec.lowered) {
          let marker = await last(); await send(`${prepared.task}

${proofFiller.repeat(spec.history)}`);
          await wait(completedAfter(marker), `${spec.name} reading`, 1, spec.files, true);
          lowered = { seq: await last(), pct: spec.lowered };
          await json(`${base}/api/config`, { method: "POST", headers, body: JSON.stringify({ context: { summary_pct: spec.lowered } }) });
          marker = await last(); await send(`${prepared.task}

You have read them already in this chat; say how many numbered files there were.`);
          await wait(completedAfter(marker), `${spec.name} lowered line`, 2, spec.files, true);
        } else if (spec.name === "full") {
          await send(prepared.task);
          await wait((events) => events.some((one) => one.type === "run.stopped"), `${spec.name} completion`, 1, spec.files);
        } else {
          let steps = 1, marker = (await eventsIn(prepared.data, id)).at(-1)?.seq || 0;
          await send(spec.plant ? `The fixture port for this chat is ${spec.plant}; I will ask for it later.

${prepared.step}` : prepared.step);
          if (spec.interrupt || spec.queue) {
            await wait((events) => events.filter((one) => one.seq > marker && one.type === "tool.result").length >= 3, `${spec.name} middle`);
            if (spec.queue) { marker = (await eventsIn(prepared.data, id)).at(-1)?.seq || marker; await send(prepared.step); steps++; }
            else {
              const stopAt = (await eventsIn(prepared.data, id)).at(-1)?.seq || marker;
              await json(`${base}/api/stop`, { method: "POST", headers, body: JSON.stringify({ session_id: id }) });
              await wait((events) => events.some((one) => one.seq > stopAt && one.type === "run.stopped"), `${spec.name} stop`);
              marker = (await eventsIn(prepared.data, id)).at(-1)?.seq || stopAt; await send(spec.continueText);
            }
          }
          await wait(completedAfter(marker), `${spec.name} completion`, 1, 1, true);
          let events = await eventsIn(prepared.data, id), first = events.find((one) => one.type === "budget" && one.data?.used_est > 0);
          const plan = drivenStepPlan(Number(first?.data?.ceiling || window), fixed(first?.data), spec.minimum); let maximum = plan.maximum, measured = false;
          const measure = () => { if (measured) return; const row = assessRun(spec.name, events, prepared.task, "", 0, spec.minimum, false, "", true).rows.find((one) => one.ran.length && one.response); if (row) { maximum = measuredStepMaximum(plan, spec.minimum, row.beforeChangeable, row.afterChangeable); measured = true; process.stdout.write(`COMPACTION PROOF STEPS MEASURED: ${spec.name} first=${plan.firstCrossing} cut=${row.beforeChangeable - row.afterChangeable} refill=${Math.ceil((row.beforeChangeable - row.afterChangeable) / plan.tokens)} minimum=${spec.minimum} plus=2 maximum=${maximum}\n`); } };
          measure();
          process.stdout.write(`COMPACTION PROOF STEPS: ${spec.name} fixed=${fixed(first?.data)} line=${Math.ceil(fixed(first?.data) + proofSummaryPct * (Number(first?.data?.ceiling || window) - fixed(first?.data)))} tokens=${plan.tokens} first=${plan.firstCrossing} maximum=${maximum}\n`);
          while (events.filter((one) => one.type === "compaction").length < spec.minimum && steps < maximum) {
            marker = events.at(-1)?.seq || 0; await send(prepared.step); steps++;
            events = await wait(completedAfter(marker), `${spec.name} step ${steps}`, steps, 1, true);
            measure();
          }
          if (spec.plant) {
            ask = { marker: await last(), detail: spec.plant }; await send("What fixture port did I give you in my first message in this chat? Answer with the number.");
            await wait(completedAfter(ask.marker), `${spec.name} ask`, steps + 1, 1, true);
          }
        }
      } catch (error) { runError = error; }
      if (runError?.notRun) {
        process.stdout.write(`
COMPACTION PROOF NOT RUN: ${spec.name} ${runError.notRun}
`);
        rows.push({ ...prepared.record, ...notRun(spec.name, runError.notRun) });
        if (evidenceRoot) await retainJournals(prepared.data, evidenceRoot, spec.name, prepared.redactions);
        continue;
      }
      const events = await eventsIn(prepared.data, id);
      const checked = assessRun(spec.name, events, prepared.task, await readFile(planted.notes, "utf8").catch((error) => error.code === "ENOENT" ? "" : Promise.reject(error)), spec.files, spec.minimum, true, runEnding(events, runError), spec.name !== "full", { lowered, ask });
      rows.push({ ...prepared.record, ...checked.result, conditions: checked.conditions });
      if (evidenceRoot) await retainJournals(prepared.data, evidenceRoot, spec.name, prepared.redactions);
      if (runError) throw runError;
    }
    return rows;
  } finally {
    eventsAbort?.abort();
    if (child && child.exitCode === null) { child.kill(); await Promise.race([new Promise((done) => child.once("exit", done)), sleep(5000)]); }
    await model?.close();
    removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction proof cleanup");
  }
}

export async function dryStart({ app, sourceData, window, stub = false }) {
  return runGroup(app, sourceData, window, [{ name: "start", files: 1 }], true, {}, "", stub);
}

// prove runs the stand-in cases and writes the record on ALL PASS (the release gate), or with
// `real` the real-model cases against the active connection: tables and evidence only, no record.
export async function prove({ app, sourceData, root = here, record, only = "", evidence = "", real = false }) {
  const started = Date.now();
  let machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8")); const active = machine.profiles.active;
  const profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8"));
  if (!real) machine = stubConnection(machine, profile);
  const plan = proofPlan(machine, profile), stub = !real;
  const sample = await dryStart({ app, sourceData, window: plan.window, stub }), timing = { startupMs: sample.startupMs, slowestStartupMs: sample.startupMs };
  process.stdout.write(`COMPACTION PROOF CALIBRATION: ${real ? "real model" : "stand-in model"}; startup ${(sample.startupMs / 1000).toFixed(3)} seconds; first scored-start allowance ${(timing.slowestStartupMs * 3 / 1000).toFixed(3)} seconds\n`);
  const selected = proofCases(plan, { real, only }), checked = [];
  if (!selected.some((run) => run.name === "needs-recall")) process.stdout.write("COMPACTION PROOF: needs-recall is shelved; it runs only with --case needs-recall\n");
  for (const run of selected) checked.push(...await runGroup(app, sourceData, plan.window, [run], false, timing, evidence, stub));
  const kinds = checked.flatMap((run) => run.kinds || []), wantKinds = real && selected.length === plan.runs.filter((run) => run.real).length ? ["prune", "prune+fresh"] : [];
  for (const kind of wantKinds.filter((one) => !kinds.includes(one))) checked.push({ run: "proof", conditions: [{ condition: 7, pass: false, turn: 0, message: `no ${kind} crossing in any run; kinds seen: ${kinds.join(", ") || "none"}` }] });
  process.stdout.write(`COMPACTION PROOF KINDS: ${kinds.join(", ") || "none"}; recall calls ${checked.reduce((sum, run) => sum + (run.recall_calls || 0), 0)}; compactions followed by recall ${checked.reduce((sum, run) => sum + (run.compactions_followed_by_recall || 0), 0)} of ${kinds.length}
`);
  process.stdout.write(`COMPACTION PROOF WALL CLOCK: ${((Date.now() - started) / 1000).toFixed(3)} seconds for ${selected.map((run) => run.name).join(", ")}\n`);
  const failure = checked.flatMap((run) => run.conditions.filter((one) => !one.pass).map((one) => ({ ...one, run: run.run }))).at(0);
  if (real) { process.stdout.write(`COMPACTION PROOF (real model, measurement only): ${failure ? `condition ${failure.condition} at turn ${failure.turn} of ${failure.run}: ${failure.message}` : "ALL PASS"}\n`); return { real: true, runs: checked }; }
  if (failure) throw new Error(`condition ${failure.condition} at turn ${failure.turn} of ${failure.run}: ${failure.message}`);
  const runs = checked.map(({ conditions, ...run }) => run);
  const identity = JSON.parse((await new Promise((done, reject) => { const child = spawn(app, ["--version"], { windowsHide: true }); let out = ""; child.stdout.on("data", (part) => out += part); child.on("error", reject); child.on("close", () => done(out)); })) || "{}");
  const value = { date: new Date().toISOString().slice(0, 10), build: { tag: identity.tag, commit: identity.commit, dirty: identity.dirty }, source_hash: await sourceHash(root), runs };
  await writeFile(record, JSON.stringify(value, null, 2) + "\n"); return value;
}

async function main() {
  const pairs = process.argv.slice(2), args = {};
  for (let i = 0; i < pairs.length; i++) { const name = pairs[i].replace(/^--/, ""), value = pairs[i + 1]; if (value === undefined || value.startsWith("--")) args[name] = true; else { args[name] = value; i++; } }
  let raw;
  if (args["raw-output"]) {
    const path = resolve(args["raw-output"]); await mkdir(dirname(path), { recursive: true }); raw = createWriteStream(path, { flags: "w" });
    for (const target of [process.stdout, process.stderr]) { const write = target.write.bind(target); target.write = (chunk, ...rest) => { raw.write(chunk); return write(chunk, ...rest); }; }
  }
  const root = resolve(args.source || here), record = resolve(args.record || join(root, "tests", "compaction-proof.json"));
  try {
    if (args.mode === "gate") { const current = await sourceHash(root), prior = JSON.parse(await readFile(record, "utf8").catch(() => "{}")); if (prior.source_hash === current) return void process.stdout.write("COMPACTION PROOF: source unchanged\n"); }
    if (args.mode === "start") { try { const sourceData = resolve(args["source-data"] || join(process.env.LOCALAPPDATA, "Agent_b")), machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8")), active = machine.profiles.active, profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8")), window = proofPlan(machine, profile).window; const result = await dryStart({ app: resolve(args.app), sourceData, window }); return void process.stdout.write(`COMPACTION PROOF START: passed window=${result.record.window} state_sessions=${result.stateExample.sessions}\n`); } catch (error) { process.stderr.write(`COMPACTION PROOF START: failed: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; return; } }
    const only = args.case || args.only || "", sourceData = resolve(args["source-data"] || join(process.env.LOCALAPPDATA, "Agent_b"));
    if (args.real) {
      try {
        if (args.mode === "gate") throw new Error("--real is a measurement and never the gate");
        if (!args.app || !args.evidence) throw new Error("--real needs --app and --evidence");
        await prove({ app: resolve(args.app), sourceData, root, record, only, evidence: resolve(args.evidence), real: true });
      } catch (error) { process.stderr.write(`COMPACTION PROOF (real model, measurement only): did not run: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; }
      return;
    }
    try { if (!args.app) throw new Error("model unreachable"); await prove({ app: resolve(args.app), sourceData, root, record, only, evidence: args.evidence ? resolve(args.evidence) : "" }); process.stdout.write("COMPACTION PROOF: passed\n"); }
    catch (error) { process.stderr.write(`BUILT, NOT RELEASED: compaction is not proved: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; }
  } finally { if (raw) await new Promise((done) => raw.end(done)); }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
