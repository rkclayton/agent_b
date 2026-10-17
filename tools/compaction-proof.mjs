import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { spawn } from "node:child_process";
import { copyFile, mkdir, mkdtemp, readFile, readdir, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { basename, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const here = resolve(fileURLToPath(new URL("..", import.meta.url)));
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));
const fixedKeys = new Set(["system", "project", "skills", "workspace_memory", "agent_memory", "tools"]);
export const startupWaitMs = Math.ceil(5357 * 1.5);
export const measuredAllowance = (startupMs, gaps = [], work = 1) => Math.ceil(Math.max(startupMs * Math.max(60, work * 12), ...gaps.map((gap) => gap * 12)));

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

async function filesBelow(directory) {
  const result = [];
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) result.push(...await filesBelow(path));
    else result.push(path);
  }
  return result;
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
export function assessRun(name, events, task, notes, fileCount, minimumCompactions, output = false) {
  const stopped = events.filter((event) => event.type === "run.stopped").at(-1);
  const compactions = events.filter((event) => event.type === "compaction");
  const lines = notes.trimEnd().split(/\r?\n/);
  const budgets = events.filter((event) => event.type === "budget" && event.data?.used_measured > 0);
  const rows = budgets.map((before, index) => {
    const end = budgets[index + 1]?.seq ?? Infinity, ran = compactions.filter((one) => one.seq > before.seq && one.seq < end);
    const after = ran.length ? budgets.find((one) => one.seq > ran.at(-1).seq) : undefined;
    const beforeChangeable = before.data.used_measured - fixed(before.data), afterChangeable = after ? after.data.used_measured - fixed(after.data) : NaN;
    return { turn: index + 1, before, after, ran, crossed: before.data.used_measured >= before.data.ceiling * .9, cut: after ? (beforeChangeable - afterChangeable) / beforeChangeable : NaN };
  });
  const around = (compact) => ({ before: budgets.filter((one) => one.seq < compact.seq).at(-1), after: budgets.find((one) => one.seq > compact.seq), turn: rows.find((one) => one.ran.includes(compact))?.turn || 0 });
  const misplaced = rows.find((one) => one.crossed !== Boolean(one.ran.length));
  const weak = compactions.map(around).find((one) => !one.before || !one.after || (one.after.data.used_measured - fixed(one.after.data)) * 2 >= one.before.data.used_measured - fixed(one.before.data));
  const expected = Array.from({ length: fileCount }, (_, index) => String(index + 1).padStart(4, "0"));
  const missingTask = compactions.map((compact) => ({ compact, turn: around(compact).turn, request: events.find((event) => event.seq > compact.seq && event.type === "model.request") })).find((one) => !JSON.stringify(one.request?.body?.messages || []).includes(task));
  const conditions = [
    { condition: 1, pass: compactions.length >= minimumCompactions && !misplaced, turn: misplaced?.turn || rows.at(-1)?.turn || 0, message: "it compacted every time it reached the line" },
    { condition: 2, pass: !weak, turn: weak?.turn || 0, message: weak && (!weak.before || !weak.after) ? "server count missing around compaction" : "compaction did not remove more than half" },
    { condition: 3, pass: stopped?.data?.reason === "done", turn: rows.at(-1)?.turn || 0, message: `run ended ${stopped?.data?.reason || "without a stop"}` },
    { condition: 4, pass: JSON.stringify(lines) === JSON.stringify(expected), turn: Math.max(1, expected.findIndex((line, index) => lines[index] !== line) + 1), message: "notes.md" },
    { condition: 5, pass: !missingTask, turn: missingTask?.turn || 0, message: "task missing after compaction" }
  ];
  if (output) {
    process.stdout.write(`\nCOMPACTION TRACE: ${name}\n| turn | server prompt | line | crossed | compaction | count after | fixed prompt | share cut |\n| ---: | ---: | ---: | :---: | :---: | ---: | ---: | ---: |\n`);
    for (const row of rows) process.stdout.write(`| ${row.turn} | ${row.before.data.used_measured} | ${Math.ceil(row.before.data.ceiling * .9)} | ${row.crossed ? "yes" : "no"} | ${row.ran.length ? `yes (${row.ran.length})` : "no"} | ${row.after?.data?.used_measured ?? "—"} | ${fixed(row.before.data)} | ${Number.isFinite(row.cut) ? (row.cut * 100).toFixed(1) + "%" : "—"} |\n`);
    process.stdout.write(`\n| condition | pass | deciding turn |\n| ---: | :---: | ---: |\n${conditions.map((one) => `| ${one.condition} | ${one.pass ? "yes" : "no"} | ${one.turn} |`).join("\n")}\n`);
  }
  const measurements = compactions.map((compact) => { const { before, after } = around(compact); return { before: before?.data?.used_measured, fixed_prompt_before: before && fixed(before.data), after: after?.data?.used_measured, fixed_prompt_after: after && fixed(after.data) }; });
  return { result: { run: name, compactions: measurements, ended: stopped?.data?.reason || "without a stop", notes_lines: lines.filter(Boolean).length }, conditions };
}
export function verifyRun(name, events, task, notes, fileCount, minimumCompactions) {
  const checked = assessRun(name, events, task, notes, fileCount, minimumCompactions), failure = checked.conditions.find((one) => !one.pass);
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

async function eventsIn(data, sessionID) {
  const files = (await filesBelow(data)).filter((file) => file.endsWith(".jsonl"));
  const values = [];
  for (const file of files) for (const line of (await readFile(file, "utf8").catch((error) => error.code === "ENOENT" ? "" : Promise.reject(error))).split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line); if (event.session_id === sessionID) values.push(event);
  }
  return values.sort((a, b) => a.seq - b.seq);
}

async function waitFor(check, label, timeout) {
  for (const end = Date.now() + timeout; Date.now() < end;) { const value = await check(); if (value) return value; await sleep(250); }
  throw new Error(`${label} timed out`);
}

async function waitForActivity(read, done, label, startupMs, work) {
  const gaps = []; let lastChange = Date.now(), lastSeq = -1, allowance = measuredAllowance(startupMs, gaps, work), deadline = lastChange + allowance;
  for (;;) {
    const values = await read(), now = Date.now(), seq = values.at(-1)?.seq || 0;
    if (done(values)) {
      process.stdout.write(`COMPACTION PROOF WAIT: ${label} allowance ${(allowance / 1000).toFixed(3)} seconds, measured from ${(startupMs / 1000).toFixed(3)}-second startup and same-run event gaps\n`);
      return values;
    }
    if (seq !== lastSeq) { if (lastSeq >= 0) gaps.push(now - lastChange); lastSeq = seq; lastChange = now; allowance = measuredAllowance(startupMs, gaps, work); deadline = now + allowance; }
    if (now >= deadline) throw new Error(`${label} timed out after measured ${(allowance / 1000).toFixed(3)}-second activity allowance`);
    await sleep(250);
  }
}

async function prepare(sourceData, root, window, fileCount) {
  const machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8"));
  const active = machine.profiles.active;
  const profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8"));
  const selected = proofSelection(machine, profile), { agent, connection, model } = selected;
  const declaredWindow = window || selected.record.window; model.context.n_ctx = declaredWindow;
  machine.connections = [connection]; machine.listen = `127.0.0.1:${await freePort()}`;
  machine.workspace = join(root, "workspace"); machine.log_dir = join(root, "data", "logs");
  machine.profiles = { active, names: [active] }; machine.context.summary_pct = .9;
  machine.run.max_turns = Math.max(machine.run.max_turns || 0, 600); machine.run.max_tool_calls = Math.max(machine.run.max_tool_calls || 0, 600);
  machine.approval.mode = "off"; machine.agents = [{ ...agent, toolset: ["read_file", "list_dir", "write_file", "edit_file"] }];
  profile.agents = [{ ...agent, toolset: ["read_file", "list_dir", "write_file", "edit_file"] }];
  await mkdir(join(root, "data", "profiles", active), { recursive: true }); await mkdir(machine.workspace, { recursive: true });
  await writeFile(join(root, "data", "harness.json"), JSON.stringify(machine));
  await writeFile(join(root, "data", "profiles", active, "profile.json"), JSON.stringify(profile));
  if (connection.credential) await copyFile(join(sourceData, `.agentb-connection-credential-${connection.credential}.dpapi`), join(root, "data", `.agentb-connection-credential-${connection.credential}.dpapi`));
  const task = "Read every numbered text file in this folder in filename order. After each read, append that file's first line to notes.md. Finish by saying how many numbered files there were.";
  const filler = "fixture words preserve a long real-model context without carrying private data. ".repeat(90);
  for (let index = 1; index <= fileCount; index++) await writeFile(join(machine.workspace, `${String(index).padStart(4, "0")}.txt`), `${String(index).padStart(4, "0")}\n${filler}\n`);
  return { active, port: Number(machine.listen.split(":").at(-1)), task, workspace: machine.workspace, data: join(root, "data"), record: { ...selected.record, window: declaredWindow }, redactions: [connection.id, connection.label, connection.base_url, connection.credential].filter(Boolean) };
}

async function runGroup(app, sourceData, window, specs, startOnly = false, timing = {}) {
  const root = await mkdtemp(join(tmpdir(), "agentb-compaction-proof-"));
  let child;
  try {
    const prepared = await prepare(sourceData, root, window, specs[0].files);
    let lastLine = "";
    const startupAt = Date.now();
    child = spawn(app, ["--config", join(prepared.data, "harness.json"), "--app-root", here, "--data-root", prepared.data], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
    child.stderr.on("data", (part) => { lastLine = String(part).trim().split(/\r?\n/).filter(Boolean).at(-1)?.replaceAll(root, "<isolated root>") || lastLine; for (const value of prepared.redactions) lastLine = lastLine.replaceAll(value, "<connection>"); });
    const base = `http://127.0.0.1:${prepared.port}`;
    let document;
    const startAllowance = timing.startupAllowanceMs || startupWaitMs;
    try { document = await waitFor(async () => { lastLine = await lastCandidateLog(prepared.data, lastLine); if (child.exitCode !== null) throw new Error(startupFailure(startAllowance, lastLine)); return fetch(`${base}/chat`).catch(() => null); }, "candidate page", startAllowance); }
    catch (error) { if (String(error.message) === "candidate page timed out") throw new Error(startupFailure(startAllowance, await lastCandidateLog(prepared.data, lastLine))); throw error; }
    const startupMs = Date.now() - startupAt;
    process.stdout.write(`COMPACTION PROOF: candidate page answered in ${(startupMs / 1000).toFixed(3)} seconds within ${(startAllowance / 1000).toFixed(3)}-second allowance\n`);
    const html = await document.text(), token = html.match(/agentb-mutation-token" content="([^"]+)/)?.[1], cookie = document.headers.get("set-cookie")?.split(";")[0] || "";
    assert.ok(token, "model unreachable"); const headers = { "Content-Type": "application/json", "X-AgentB-Mutation-Token": token, Cookie: cookie };
    if (startOnly) return { record: prepared.record, startupMs };
    const activityBase = Math.max(timing.startupMs || 0, startupMs);
    const rows = [];
    for (const spec of specs) {
      const created = await json(`${base}/api/sessions`, { method: "POST", headers, body: JSON.stringify({ agent_id: "agent_b" }) });
      const id = created.session.id, send = (text) => json(`${base}/api/message`, { method: "POST", headers, body: JSON.stringify({ session_id: id, text }) });
      let runError;
      try {
        await send(prepared.task);
        if (spec.interrupt || spec.queue) {
          await waitForActivity(() => eventsIn(prepared.data, id), (events) => events.filter((one) => one.type === "tool.result").length >= 3, `${spec.name} middle`, activityBase, spec.files);
          if (spec.queue) await send("Keep working on the same task.");
          else { await json(`${base}/api/stop`, { method: "POST", headers, body: JSON.stringify({ session_id: id }) }); await waitForActivity(() => eventsIn(prepared.data, id), (events) => events.some((one) => one.type === "run.stopped"), `${spec.name} stop`, activityBase, spec.files); await send(spec.continueText); }
        }
        await waitForActivity(() => eventsIn(prepared.data, id), (events) => events.some((one) => one.type === "run.stopped"), `${spec.name} completion`, activityBase, spec.files);
      } catch (error) { runError = error; }
      const checked = assessRun(spec.name, await eventsIn(prepared.data, id), prepared.task, await readFile(join(prepared.workspace, "notes.md"), "utf8").catch((error) => error.code === "ENOENT" ? "" : Promise.reject(error)), spec.files, spec.minimum, true);
      rows.push({ ...prepared.record, ...checked.result, conditions: checked.conditions });
      await writeFile(join(prepared.workspace, "notes.md"), "");
      if (runError) throw runError;
    }
    return rows;
  } finally {
    if (child && child.exitCode === null) { child.kill(); await Promise.race([new Promise((done) => child.once("exit", done)), sleep(5000)]); }
    removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction proof cleanup");
  }
}

export async function dryStart({ app, sourceData, window }) {
  return runGroup(app, sourceData, window, [{ name: "start", files: 1 }], true);
}

export async function prove({ app, sourceData, root = here, record, only = "" }) {
  const short = [{ name: "fresh", files: 60, minimum: 3 }, { name: "typed-continue", files: 60, minimum: 3, interrupt: true, continueText: "continue" }, { name: "Continue", files: 60, minimum: 3, interrupt: true, continueText: "Continue" }, { name: "queued-message", files: 60, minimum: 3, queue: true }];
  const wanted = new Set(only ? only.split(",") : [...short.map((one) => one.name), "full"]);
  const machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8")), active = machine.profiles.active;
  const profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8")), ownWindow = proofSelection(machine, profile).record.window;
  const sample = await dryStart({ app, sourceData, window: 48000 }), timing = { startupMs: sample.startupMs, startupAllowanceMs: Math.ceil(sample.startupMs * 1.5) };
  process.stdout.write(`COMPACTION PROOF CALIBRATION: startup ${(sample.startupMs / 1000).toFixed(3)} seconds; scored-start allowance ${(timing.startupAllowanceMs / 1000).toFixed(3)} seconds\n`);
  const selectedShort = short.filter((one) => wanted.has(one.name)), checked = [
    ...(selectedShort.length ? await runGroup(app, sourceData, 48000, selectedShort, false, timing) : []),
    ...(wanted.has("full") ? await runGroup(app, sourceData, ownWindow, [{ name: "full", files: Math.ceil(40 * ownWindow / 32768), minimum: 1 }], false, timing) : [])
  ];
  const failure = checked.flatMap((run) => run.conditions.filter((one) => !one.pass).map((one) => ({ ...one, run: run.run }))).at(0);
  if (failure) throw new Error(`condition ${failure.condition} at turn ${failure.turn} of ${failure.run}: ${failure.message}`);
  const runs = checked.map(({ conditions, ...run }) => run);
  const identity = JSON.parse((await new Promise((done, reject) => { const child = spawn(app, ["--version"], { windowsHide: true }); let out = ""; child.stdout.on("data", (part) => out += part); child.on("error", reject); child.on("close", () => done(out)); })) || "{}");
  const value = { date: new Date().toISOString().slice(0, 10), build: { tag: identity.tag, commit: identity.commit, dirty: identity.dirty }, source_hash: await sourceHash(root), runs };
  await writeFile(record, JSON.stringify(value, null, 2) + "\n"); return value;
}

async function main() {
  const pairs = process.argv.slice(2), args = {}; for (let i = 0; i < pairs.length; i += 2) args[pairs[i].replace(/^--/, "")] = pairs[i + 1];
  const root = resolve(args.source || here), record = resolve(args.record || join(root, "tests", "compaction-proof.json"));
  if (args.mode === "gate") { const current = await sourceHash(root), prior = JSON.parse(await readFile(record, "utf8").catch(() => "{}")); if (prior.source_hash === current) return void process.stdout.write("COMPACTION PROOF: source unchanged\n"); }
  if (args.mode === "start") { try { const result = await dryStart({ app: resolve(args.app), sourceData: resolve(args["source-data"] || join(process.env.LOCALAPPDATA, "Agent_b")), window: Number(args.window || 48000) }); return void process.stdout.write(`COMPACTION PROOF START: passed window=${result.record.window}\n`); } catch (error) { process.stderr.write(`COMPACTION PROOF START: failed: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; return; } }
  try { if (!args.app) throw new Error("model unreachable"); await prove({ app: resolve(args.app), sourceData: resolve(args["source-data"] || join(process.env.LOCALAPPDATA, "Agent_b")), root, record, only: args.only }); process.stdout.write("COMPACTION PROOF: passed\n"); }
  catch (error) { process.stderr.write(`BUILT, NOT RELEASED: compaction is not proved: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
