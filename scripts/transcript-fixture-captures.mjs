// Item 2gz, v1.2.4/W2 — the transcript captures render a checked-in journal.
//
// A live run's transcript is not a pure function of the build: how far a run
// got, how many thoughts a turn produced, what the token counts measured, all
// move rows. v1.2.2 fixed four such races and the fifth - a turn rendering one
// thought or two, driven by measured token counts - reflowed every row below it
// and could not be masked, because a mask hides a value and cannot hide a
// reflow. So the gate stops photographing live runs: it renders a FIXED JOURNAL
// through the replay route, which 2gs proved is a pure function of that journal.
// Two captures of one build match, or the difference is a real change.
//
//   node scripts/transcript-fixture-captures.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir> [--fixtures <dir>]
//
// The captures carry NO masks: there is nothing live left in them to mask.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "@playwright/test";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
const fixtureDir = resolve(args.fixtures || "tests/fixtures/transcripts");
await mkdir(evidence, { recursive: true });

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

async function freePort() {
  const probe = createServer();
  await new Promise((done) => probe.listen(0, "127.0.0.1", done));
  const port = probe.address().port;
  await new Promise((done) => probe.close(done));
  assert.ok(port !== 8790, "a fixture render must not take production's port");
  return port;
}

async function sessionOf(path) {
  const text = await readFile(path, "utf8");
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const id = JSON.parse(line).session_id;
    if (id) return id;
  }
  throw new Error(`no session_id in ${path}`);
}

const fixtures = (await readdir(fixtureDir)).filter((name) => name.endsWith(".jsonl")).sort();
assert.ok(fixtures.length, `no journal fixtures in ${fixtureDir}`);

const results = [];
let failures = 0;
const browser = await chromium.launch({ channel: "msedge", headless: true });
try {
  for (const fixture of fixtures) {
    const name = fixture.replace(/\.jsonl$/, "");
    const path = join(fixtureDir, fixture);
    const session = await sessionOf(path);
    const port = await freePort();
    const dataRoot = join(resolve(args.data), name);
    await mkdir(dataRoot, { recursive: true });
    const configPath = join(dataRoot, "harness.json");
    await writeFile(configPath, `${JSON.stringify({ config_version: 6, listen: `127.0.0.1:${port}`, workspace: dataRoot, log_dir: join(dataRoot, "logs"), connections: [], services: {}, agents: [], chat: { auto_rename: false } }, null, 2)}\n`);

    const app = spawn(resolve(args.exe), ["-config", configPath, "-app-root", resolve(args["app-root"]), "-data-root", dataRoot, "-replay", path], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
    let stderr = "";
    app.stderr.on("data", (chunk) => { stderr += chunk; });
    try {
      const base = `http://127.0.0.1:${port}`;
      const deadline = Date.now() + 30000;
      let ready = false;
      while (Date.now() < deadline && !ready) {
        try { ready = (await fetch(`${base}/api/state`)).ok; } catch { await sleep(50); }
      }
      assert.ok(ready, `the replay server did not become ready for ${fixture}: ${stderr.slice(-400)}`);

      const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
      const page = await context.newPage();
      await page.goto(`${base}/chat?session=${encodeURIComponent(session)}`);
      await page.locator(".chat-entry").first().waitFor({ timeout: 20000 });
      // The replay feeds the journal over time. Before anything is measured,
      // wait for the server to stop moving through it AND for the page to have
      // applied what the server has: the same two-reads-agree rule the chat
      // acceptance uses before it swaps in a fixture.
      {
        const cursorOf = async () => {
          const state = await (await fetch(`${base}/api/state`)).json();
          return JSON.stringify(state.sessions?.[session]?.cursor || null);
        };
        const clientCursor = () => page.evaluate(async (id) => {
          const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
          return JSON.stringify(bus.store.sessions[id]?.cursor || null);
        }, session);
        const until = Date.now() + 30000;
        let previous = "";
        while (Date.now() < until) {
          const server = await cursorOf();
          if (server === previous && (await clientCursor()) === server) break;
          previous = server;
          await sleep(150);
        }
      }
      // A fixture ends with its run stopped, so nothing should be in flight -
      // but the replay feeds its records over time, and a capture taken while
      // they are still arriving photographs whichever stage line had arrived.
      // Two renders of one fixture differed by exactly that. So the page is
      // asked to settle: the transcript, the notice and the readout unchanged
      // across ten frames.
      await page.evaluate(() => new Promise((done) => {
        const read = () => JSON.stringify([
          document.getElementById("chat-log")?.innerText || "",
          document.getElementById("chat-notice")?.innerText || "",
          document.getElementById("chat-readout-figures")?.innerText || "",
        ]);
        let previous = "";
        let steady = 0;
        const step = () => {
          const now = read();
          steady = now === previous ? steady + 1 : 0;
          previous = now;
          if (steady >= 10) return done();
          requestAnimationFrame(step);
        };
        requestAnimationFrame(step);
        setTimeout(done, 15000);
      }));
      // The journal is fixed and nothing streams, so the only thing left that
      // could move is where the transcript is standing. It is put at its foot,
      // which is where a chat stands when its last line arrives.
      await page.evaluate(() => new Promise((done) => {
        const log = document.getElementById("chat-log");
        if (!log) return done();
        let previous = -1;
        let steady = 0;
        const step = () => {
          log.scrollTop = log.scrollHeight;
          steady = log.scrollTop === previous ? steady + 1 : 0;
          previous = log.scrollTop;
          if (steady >= 10) return done();
          requestAnimationFrame(step);
        };
        requestAnimationFrame(step);
        setTimeout(done, 5000);
      }));
      await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
      const entries = await page.locator(".chat-entry").count();
      // The TRANSCRIPT is what this fixture is for, so the transcript is what
      // is photographed. Taking the whole page would drag in the composer,
      // whose rounded edge antialiases a level apart between runs - a known
      // non-live difference that the live captures mask by name and that has
      // no business being masked in here.
      await page.locator("#chat-log").screenshot({ path: join(evidence, `transcript-${name}.png`), animations: "disabled" });
      results.push({ fixture, session, entries });
      process.stdout.write(`PASS transcript-${name} (${entries} entries)\n`);
      await context.close();
    } catch (error) {
      failures++;
      results.push({ fixture, error: error.message });
      process.stdout.write(`FAIL transcript-${name} — ${error.message}\n`);
    } finally {
      // Hard stop (12): a process this script started is ended by its PID.
      if (app.exitCode === null) {
        app.kill();
        await Promise.race([new Promise((done) => app.once("exit", done)), sleep(3000)]);
      }
    }
  }
} finally {
  await browser.close().catch(() => {});
}

await writeFile(join(evidence, "transcript-fixtures.json"), `${JSON.stringify({ fixtureDir, results }, null, 1)}\n`);
process.stdout.write(`${failures ? "TRANSCRIPT FIXTURES FAIL" : "TRANSCRIPT FIXTURES PASS"} ${results.length - failures} of ${results.length}\n`);
process.exitCode = failures ? 1 : 0;
