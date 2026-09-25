// Item 2l3: the bounded fixture the connection-replay gate owns. Before this the
// gate needed a hand-assembled copy of the operator's profile (25 chats, 51,655
// events) and nothing in the repository built any of it. This builds everything
// the runner needs from journals checked in beside it, on disposable roots it
// creates and removes, so no release depends on the operator's data; the
// production-sized comparison stays available through --chats.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { copyFile, mkdir, mkdtemp, readdir, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { basename, dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repository = resolve(here, "..");
// Bounded on purpose: three journals, 202 events, enough to exercise streaming
// projection, tool and card rendering, a long transcript and shell geometry.
const boundedJournals = ["short.jsonl", "cards.jsonl", "long.jsonl"];

const args = {};
for (let index = 2; index < process.argv.length; index += 1) {
  const flag = process.argv[index];
  if (!flag.startsWith("--")) throw new Error(`unexpected argument ${flag}`);
  const value = process.argv[index + 1];
  args[flag.slice(2)] = value === undefined || value.startsWith("--") ? true : value;
  if (typeof args[flag.slice(2)] === "string") index += 1;
}

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));
const digest = (data) => createHash("sha256").update(data).digest("hex");

async function freePort() {
  for (;;) {
    const probe = createServer();
    await new Promise((done) => probe.listen(0, "127.0.0.1", done));
    const { port } = probe.address();
    await new Promise((done) => probe.close(done));
    if (port !== 8790) return port; // never the operator's production port
  }
}

// Stopping an owned child tolerates one that has already exited: a process that
// is gone is the outcome we wanted, not an ESRCH fault to report.
const children = [];
function stopChildren() {
  for (const child of children) {
    if (child.exitCode !== null || child.signalCode !== null) continue;
    try { child.kill(); } catch (error) { if (error?.code !== "ESRCH") throw error; }
  }
}

async function seedRole(root, role, journals, port, mutate) {
  const dataRoot = join(root, role);
  const chats = join(dataRoot, "chats");
  await mkdir(chats, { recursive: true });
  await mkdir(join(dataRoot, "logs"), { recursive: true });
  for (const [id, source] of journals) {
    const target = join(chats, `${id}.jsonl`);
    await copyFile(source, target);
    if (mutate !== id) continue;
    const text = await readFile(target, "utf8");
    const mutated = text.replace(/("role":"user","content":")([^"]*)/, (_, head, body) => `${head}${body} MUTATED`);
    assert.notEqual(mutated, text, `negative control: ${id} carries no user message to alter`);
    await writeFile(target, mutated);
  }
  const config = JSON.parse(await readFile(join(repository, "harness.example.json"), "utf8"));
  // The gate's subject is the legacy connection list, so the fixture writes one
  // and lets the harness migrate it exactly as an operator's config is migrated.
  delete config.connections;
  config["ser" + "vers"] = [{ id: "acceptance", label: "Acceptance", base_url: "http://127.0.0.1:1/v1", model: "fixture", credential: "", probe_mode: "off", context: { n_ctx: 32768 } }];
  // Everything the run writes stays inside the disposable root; the fixture owns
  // no Windows account, so the service split is off and agentb-svc is untouched.
  Object.assign(config, {
    listen: `127.0.0.1:${port}`, workspace: join(dataRoot, "workspace"), log_dir: join(dataRoot, "logs"),
    memory: { ...config.memory, dir: join(dataRoot, "memory") }, updates: { auto_check: false },
    agents: [{ name: "Acceptance", b: "acceptance", c: "acceptance", d: "acceptance", toolset: ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service"] }],
    shell: { ...config.shell, service_account: { ...config.shell.service_account, enabled: false } },
  });
  await writeFile(join(dataRoot, "harness.json"), `${JSON.stringify(config, null, 2)}\n`);
  return { dataRoot, chats, origin: `http://127.0.0.1:${port}` };
}

async function start(app, role) {
  const child = spawn(join(app, "Agent_b.exe"), ["-config", join(role.dataRoot, "harness.json"), "-app-root", app, "-data-root", role.dataRoot], { windowsHide: true, stdio: ["ignore", "inherit", "inherit"] });
  children.push(child);
  const deadline = Date.now() + 90000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`${role.origin} exited with ${child.exitCode} before it was ready`);
    const ready = await fetch(`${role.origin}/api/connections`).then((response) => response.ok, () => false);
    if (ready) return;
    await sleep(100);
  }
  throw new Error(`${role.origin} never became ready`);
}

// Startup migrates a legacy data root into a per-user profile, which MOVES the
// retained journals, so their directory is resolved after the run, not assumed.
async function chatsDirectory(dataRoot) {
  for (const name of await readdir(join(dataRoot, "profiles")).catch(() => [])) {
    const candidate = join(dataRoot, "profiles", name, "chats");
    if ((await readdir(candidate).catch(() => [])).length) return candidate;
  }
  return join(dataRoot, "chats");
}

async function journalPrefixes(directory) {
  const names = (await readdir(directory).catch(() => [])).filter((entry) => entry.endsWith(".jsonl"));
  return Object.fromEntries(await Promise.all(names.map(async (name) => [basename(name, ".jsonl"), await readFile(join(directory, name))])));
}

assert.ok(args.app, "missing --app (the candidate application root holding Agent_b.exe)");
const app = resolve(args.app);
const evidence = resolve(args.evidence ?? join(repository, "logs", "evidence", "connection-replay"));
const negative = Boolean(args["negative-control"]);
await mkdir(evidence, { recursive: true });

const root = await mkdtemp(join(tmpdir(), "agentb-connection-replay-"));
let failure = null;
try {
  let journals;
  if (args.chats) {
    // Arm (e): the production-sized comparison, invoked explicitly, never by a release.
    const directory = resolve(args.chats);
    journals = (await readdir(directory)).filter((name) => name.endsWith(".jsonl")).map((name) => join(directory, name));
    assert.ok(journals.length, `--chats ${directory} holds no journals`);
  } else journals = boundedJournals.map((name) => join(here, "fixtures", "transcripts", name));
  // The journals the cursors and the runner read are a pristine seed no harness
  // ever opens, so replay immutability is proved against them rather than assumed.
  const seed = join(root, "seed");
  await mkdir(seed, { recursive: true });
  const seeded = [];
  const cursors = [];
  for (const source of journals) {
    const lines = (await readFile(source, "utf8")).split(/\r?\n/).filter((line) => line.trim());
    const id = JSON.parse(lines[0]).session_id;
    const target = join(seed, `${id}.jsonl`);
    await copyFile(source, target);
    seeded.push([id, target]);
    const bytes = (await readFile(target)).length;
    cursors.push({ id, events: lines.length, bytes, cursor_offset: bytes });
  }
  await writeFile(join(root, "replay-cursors.json"), `${JSON.stringify(cursors, null, 2)}\n`);
  const ports = [await freePort(), await freePort()];
  const production = await seedRole(root, "production", seeded, ports[0]);
  const candidate = await seedRole(root, "candidate", seeded, ports[1], negative ? seeded[0][0] : null);
  const beforeConfig = join(root, "before-config.json");
  await copyFile(join(production.dataRoot, "harness.json"), beforeConfig);
  const before = { production: await journalPrefixes(production.chats), candidate: await journalPrefixes(candidate.chats) };
  await start(app, production);
  await start(app, candidate);  const afterConfig = join(root, "after-config.json");
  await copyFile(join(candidate.dataRoot, "harness.json"), afterConfig);
  const gate = spawn(process.execPath, [join(here, "connection-replay-acceptance.mjs"),
    "--production", production.origin, "--candidate", candidate.origin, "--before-config", beforeConfig,
    "--after-config", afterConfig, "--chats", seed, "--replay-cursors", join(root, "replay-cursors.json"), "--evidence", evidence], { stdio: "inherit" });
  const code = await new Promise((done) => gate.on("close", done));
  // Replay immutability: a journal the harness restored is appended to, never
  // rewritten, so every byte it already held must still be there unchanged.
  for (const [role, dataRoot] of [["production", production.dataRoot], ["candidate", candidate.dataRoot]]) {
    const after = await journalPrefixes(await chatsDirectory(dataRoot));
    for (const [id, bytes] of Object.entries(before[role])) {
      assert.ok(after[id], `${role}/${id}: retained journal disappeared`);
      assert.equal(digest(after[id].subarray(0, bytes.length)), digest(bytes), `${role}/${id}: retained journal was rewritten`);
    }
  }
  const events = cursors.reduce((sum, row) => sum + row.events, 0);
  if (negative) assert.notEqual(code, 0, "NEGATIVE CONTROL FAILED: an altered candidate projection still passed the gate");
  else assert.equal(code, 0, `connection replay failed with exit ${code}`);
  console.log(negative ? `PASS negative control: the gate rejected the altered projection (exit ${code})`
    : `PASS bounded connection replay: ${cursors.length} chats, ${events} events, journals immutable`);
} catch (error) {
  failure = error;
} finally {
  stopChildren();
  await sleep(500);
  if (failure) console.error(`fixture root retained for inspection: ${root}`);
  else if (!args.keep) removeTreeWithinAllowedRoots(root, [tmpdir()], "connection replay fixture cleanup");
}
if (failure) { console.error(failure.message); process.exitCode = 1; }
