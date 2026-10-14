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
export function verifyRun(name, events, task, notes, fileCount, minimumCompactions) {
  const compactions = events.filter((event) => event.type === "compaction");
  assert.ok(compactions.length >= minimumCompactions, `${name}: it compacted every time it reached the line`);
  const stopped = events.filter((event) => event.type === "run.stopped").at(-1);
  assert.equal(stopped?.data?.reason, "done", `${name}: run ended ${stopped?.data?.reason || "without a stop"}`);
  const lines = notes.trimEnd().split(/\r?\n/);
  assert.deepEqual(lines, Array.from({ length: fileCount }, (_, index) => String(index + 1).padStart(4, "0")), `${name}: notes.md`);
  const measurements = [];
  for (const compact of compactions) {
    const before = events.filter((event) => event.seq < compact.seq && event.type === "budget" && event.data?.used_measured > 0).at(-1);
    const after = events.find((event) => event.seq > compact.seq && event.type === "budget" && event.data?.used_measured > 0);
    assert.ok(before && after, `${name}: server count missing around compaction`);
    const beforeChangeable = before.data.used_measured - fixed(before.data), afterChangeable = after.data.used_measured - fixed(after.data);
    assert.ok(before.data.used_measured >= before.data.ceiling * .9, `${name}: compaction below the line`);
    assert.ok(afterChangeable * 2 < beforeChangeable, `${name}: compaction did not remove more than half`);
    const nextRequest = events.find((event) => event.seq > compact.seq && event.type === "model.request");
    assert.ok(JSON.stringify(nextRequest?.body?.messages || []).includes(task), `${name}: task missing after compaction`);
    measurements.push({ before: before.data.used_measured, fixed_prompt_before: fixed(before.data), after: after.data.used_measured, fixed_prompt_after: fixed(after.data) });
  }
  return { run: name, compactions: measurements, ended: "done", notes_lines: lines.length };
}

async function freePort() {
  const server = createServer();
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const port = server.address().port;
  await new Promise((done) => server.close(done));
  return port;
}

async function json(url, options = {}) {
  const response = await fetch(url, options), text = await response.text();
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

async function waitFor(check, label, timeout = 45 * 60_000) {
  for (const end = Date.now() + timeout; Date.now() < end;) { const value = await check(); if (value) return value; await sleep(250); }
  throw new Error(`${label} timed out`);
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

async function runGroup(app, sourceData, window, specs) {
  const root = await mkdtemp(join(tmpdir(), "agentb-compaction-proof-"));
  let child;
  try {
    const prepared = await prepare(sourceData, root, window, specs[0].files);
    let lastLine = "";
    child = spawn(app, ["--config", join(prepared.data, "harness.json"), "--app-root", here, "--data-root", prepared.data], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
    child.stderr.on("data", (part) => { lastLine = String(part).trim().split(/\r?\n/).filter(Boolean).at(-1)?.replaceAll(root, "<isolated root>") || lastLine; for (const value of prepared.redactions) lastLine = lastLine.replaceAll(value, "<connection>"); });
    const base = `http://127.0.0.1:${prepared.port}`;
    let document;
    try { document = await waitFor(async () => { lastLine = await lastCandidateLog(prepared.data, lastLine); if (child.exitCode !== null) throw new Error(startupFailure(startupWaitMs, lastLine)); return fetch(`${base}/chat`).catch(() => null); }, "candidate page", startupWaitMs); }
    catch (error) { if (String(error.message) === "candidate page timed out") throw new Error(startupFailure(startupWaitMs, await lastCandidateLog(prepared.data, lastLine))); throw error; }
    const html = await document.text(), token = html.match(/agentb-mutation-token" content="([^"]+)/)?.[1], cookie = document.headers.get("set-cookie")?.split(";")[0] || "";
    assert.ok(token, "model unreachable"); const headers = { "Content-Type": "application/json", "X-AgentB-Mutation-Token": token, Cookie: cookie };
    const rows = [];
    for (const spec of specs) {
      const created = await json(`${base}/api/sessions`, { method: "POST", headers, body: JSON.stringify({ agent_id: "agent_b" }) });
      const id = created.session.id, send = (text) => json(`${base}/api/message`, { method: "POST", headers, body: JSON.stringify({ session_id: id, text }) });
      await send(prepared.task);
      if (spec.interrupt || spec.queue) {
        await waitFor(async () => (await eventsIn(prepared.data, id)).filter((one) => one.type === "tool.result").length >= 3, `${spec.name} middle`);
        if (spec.queue) await send("Keep working on the same task.");
        else { await json(`${base}/api/stop`, { method: "POST", headers, body: JSON.stringify({ session_id: id }) }); await waitFor(async () => (await eventsIn(prepared.data, id)).some((one) => one.type === "run.stopped"), `${spec.name} stop`); await send(spec.continueText); }
      }
      await waitFor(async () => (await eventsIn(prepared.data, id)).filter((one) => one.type === "run.stopped").at(-1)?.data?.reason === "done", `${spec.name} completion`);
      rows.push({ ...prepared.record, ...verifyRun(spec.name, await eventsIn(prepared.data, id), prepared.task, await readFile(join(prepared.workspace, "notes.md"), "utf8"), spec.files, spec.minimum) });
      await writeFile(join(prepared.workspace, "notes.md"), "");
    }
    return rows;
  } finally {
    if (child && child.exitCode === null) { child.kill(); await Promise.race([new Promise((done) => child.once("exit", done)), sleep(5000)]); }
    removeTreeWithinAllowedRoots(root, [tmpdir()], "compaction proof cleanup");
  }
}

export async function prove({ app, sourceData, root = here, record }) {
  const short = [{ name: "fresh", files: 60, minimum: 3 }, { name: "typed-continue", files: 60, minimum: 3, interrupt: true, continueText: "continue" }, { name: "Continue", files: 60, minimum: 3, interrupt: true, continueText: "Continue" }, { name: "queued-message", files: 60, minimum: 3, queue: true }];
  const machine = JSON.parse(await readFile(join(sourceData, "harness.json"), "utf8")), active = machine.profiles.active;
  const profile = JSON.parse(await readFile(join(sourceData, "profiles", active, "profile.json"), "utf8")), ownWindow = proofSelection(machine, profile).record.window;
  const runs = [...await runGroup(app, sourceData, 48000, short), ...await runGroup(app, sourceData, ownWindow, [{ name: "full", files: Math.ceil(40 * ownWindow / 32768), minimum: 1 }])];
  const identity = JSON.parse((await new Promise((done, reject) => { const child = spawn(app, ["--version"], { windowsHide: true }); let out = ""; child.stdout.on("data", (part) => out += part); child.on("error", reject); child.on("close", () => done(out)); })) || "{}");
  const value = { date: new Date().toISOString().slice(0, 10), build: { tag: identity.tag, commit: identity.commit, dirty: identity.dirty }, source_hash: await sourceHash(root), runs };
  await writeFile(record, JSON.stringify(value, null, 2) + "\n"); return value;
}

async function main() {
  const pairs = process.argv.slice(2), args = {}; for (let i = 0; i < pairs.length; i += 2) args[pairs[i].replace(/^--/, "")] = pairs[i + 1];
  const root = resolve(args.source || here), record = resolve(args.record || join(root, "tests", "compaction-proof.json"));
  if (args.mode === "gate") { const current = await sourceHash(root), prior = JSON.parse(await readFile(record, "utf8").catch(() => "{}")); if (prior.source_hash === current) return void process.stdout.write("COMPACTION PROOF: source unchanged\n"); }
  try { if (!args.app) throw new Error("model unreachable"); await prove({ app: resolve(args.app), sourceData: resolve(args["source-data"] || join(process.env.LOCALAPPDATA, "Agent_b")), root, record }); process.stdout.write("COMPACTION PROOF: passed\n"); }
  catch (error) { process.stderr.write(`BUILT, NOT RELEASED: compaction is not proved: ${String(error.message).replace(/[\r\n]+/g, " ")}\n`); process.exitCode = 1; }
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) await main();
