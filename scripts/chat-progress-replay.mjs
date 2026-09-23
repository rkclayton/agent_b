import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:net";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "playwright";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: Math.floor(argv.length / 2) }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const key of ["app", "config", "data", "replay", "evidence", "samples"]) assert.ok(args[key], `missing --${key}`);
const samples = args.samples.split(",").map((value) => value.trim()).filter(Boolean);
assert.ok(samples.length, "--samples is empty");
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

async function freePort() {
  const server = createServer();
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const port = server.address().port;
  await new Promise((done) => server.close(done));
  return port;
}

async function waitState(base, timeout = 30000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`${base}/api/state`);
      if (response.ok) return response.json();
    } catch {}
    await sleep(50);
  }
  throw new Error(`timed out waiting for ${base}/api/state`);
}

const source = (await readFile(resolve(args.replay), "utf8")).split(/\r?\n/).filter(Boolean).map((line) => ({ line, event: JSON.parse(line) }));
const baseConfig = JSON.parse(await readFile(resolve(args.config), "utf8"));
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: false });
const results = [];

for (const [index, at] of samples.entries()) {
  const end = `${at}.999Z`;
  const selected = source.filter(({ event }) => String(event.ts || "") <= end);
  assert.ok(selected.length, `no replay events through ${at}`);
  const sampleRoot = resolve(args.data, `sample-${index + 1}`);
  await mkdir(sampleRoot, { recursive: true });
  const tape = join(sampleRoot, "sample.jsonl");
  await writeFile(tape, `${selected.map(({ line }) => line).join("\n")}\n`);
  const port = await freePort();
  const config = { ...baseConfig, listen: `127.0.0.1:${port}`, log_dir: join(sampleRoot, "logs") };
  const configPath = join(sampleRoot, "harness.json");
  await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`);
  const app = spawn(resolve(args.app, "Agent_b.exe"), ["-config", configPath, "-app-root", resolve(args.app), "-data-root", sampleRoot, "-replay", tape], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
  let stderr = "";
  app.stderr.on("data", (chunk) => { stderr += String(chunk); });
  let browser;
  try {
    const base = `http://127.0.0.1:${port}`;
    const state = await waitState(base);
    assert.ok(state.sessions?.s3, `s3 missing at ${at}: ${stderr}`);
    browser = await chromium.launch({ channel: "msedge", headless: true });
    const context = await browser.newContext({ viewport: { width: 1250, height: 975 }, deviceScaleFactor: 1 });
    await context.addInitScript(() => {
      window.__progressPatchCount = 0;
      const NativeEventSource = window.EventSource;
      window.EventSource = class extends NativeEventSource {
        constructor(url, options) {
          const parsed = new URL(url, location.href);
          if (parsed.pathname === "/api/events") parsed.searchParams.set("instant", "1");
          super(parsed.href, options);
          this.addEventListener("projection.patch", () => { window.__progressPatchCount++; });
        }
      };
    });
    const page = await context.newPage();
    await page.goto(`${base}/chat?session=s3`, { waitUntil: "domcontentloaded" });
    const finalCursor = state.sessions.s3.cursor;
    await page.waitForFunction(async (cursor) => {
      const script = document.querySelector("script[src*='/js/build-check.js']");
      if (!script) return false;
      const { store } = await import(new URL("bus.js", script.src).href);
      const current = store.sessions?.s3?.cursor;
      return current?.generation === cursor.generation && Number(current?.offset || 0) === Number(cursor.offset || 0);
    }, finalCursor, { timeout: 30000 });
    await page.waitForFunction((count) => window.__progressPatchCount >= count, selected.length, { timeout: 30000 });
    await page.waitForFunction(() => document.querySelector("#chat-notice .chat-notice-text")?.textContent?.length > 0);
    await page.waitForTimeout(100);
    const ui = await page.evaluate(() => {
      const send = document.querySelector("#chat-send");
      const rect = send?.getBoundingClientRect();
      const visible = !!rect && rect.width > 0 && rect.height > 0 && rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight;
      return {
        status: document.querySelector("#chat-notice .chat-notice-text")?.textContent || "",
        tool_rows: [...document.querySelectorAll(".chat-response-tool .tool-tick")].map((node) => node.textContent.trim()),
        response_headers: [...document.querySelectorAll(".chat-response-summary")].filter((node) => !node.hidden).map((node) => node.textContent.trim()),
        steps_headers: [...document.querySelectorAll(".chat-step-summary")].filter((node) => !node.hidden).map((node) => node.textContent.trim()),
        stop: { mode: send?.dataset.state || "", visible, rect: rect ? { x: rect.x, y: rect.y, width: rect.width, height: rect.height } : null },
        viewport: { width: innerWidth, height: innerHeight },
      };
    });
    const last = selected.at(-1).event;
    const result = { at, last_event: { seq: last.seq, ts: last.ts, type: last.type }, ui };
    results.push(result);
    await page.screenshot({ path: join(evidence, `${at.replaceAll(":", "")}.png`) });
  } finally {
    await browser?.close().catch(() => {});
    try { app.kill(); } catch {}
    await sleep(150);
  }
}

await writeFile(join(evidence, "result.json"), `${JSON.stringify({ replay: resolve(args.replay), samples: results }, null, 2)}\n`);
process.stdout.write(`${JSON.stringify(results, null, 2)}\n`);
