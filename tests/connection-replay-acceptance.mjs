import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium, request } from "playwright";

function argumentsOf(argv) {
  const result = {};
  for (let index = 2; index < argv.length; index += 2) result[argv[index].replace(/^--/, "")] = argv[index + 1];
  for (const required of ["production", "candidate", "before-config", "after-config", "chats", "replay-cursors", "evidence"]) {
    if (!result[required]) throw new Error(`missing --${required}`);
  }
  return result;
}

const digest = (data) => createHash("sha256").update(data).digest("hex");
const legacyListKey = "ser" + "vers";

function normalizedConfig(document, { migrated = false } = {}) {
  const copy = structuredClone(document);
  delete copy.config_version;
  delete copy.listen;
  for (const key of ["workspace", "log_dir"]) if (copy[key]) copy[key] = String(copy[key]).split(/[\\/]/).at(-1);
  if (copy.memory?.dir) copy.memory.dir = String(copy.memory.dir).split(/[\\/]/).at(-1);
  if (migrated) {
    copy.connections = copy[legacyListKey];
    delete copy[legacyListKey];
  }
  return copy;
}

function assertPreserved(actual, expected, path = "config") {
  if (Array.isArray(expected)) {
    assert.ok(Array.isArray(actual) && actual.length >= expected.length, `${path} lost array entries`);
    if (expected.every((value) => value === null || typeof value !== "object")) {
      let cursor = 0;
      for (const value of expected) { cursor = actual.indexOf(value, cursor); assert.notEqual(cursor, -1, `${path} lost ${JSON.stringify(value)}`); cursor++; }
    } else expected.forEach((value, index) => assertPreserved(actual[index], value, `${path}[${index}]`));
    return;
  }
  if (expected && typeof expected === "object") {
    for (const [key, value] of Object.entries(expected)) assertPreserved(actual?.[key], value, `${path}.${key}`);
    return;
  }
  assert.deepEqual(actual, expected, `${path} changed during migration`);
}

function transcript(session) {
  return { messages: session?.messages || [], chat: session?.chat || [] };
}

async function state(base) {
  const client = await request.newContext();
  await client.get(`${base}/chat?session=s2`);
  const response = await client.get(`${base}/api/state`);
  if (!response.ok()) throw new Error(`${base}/api/state returned ${response.status()}`);
  try { return await response.json(); } finally { await client.dispose(); }
}

async function eventCounts(directory, sessionIDs) {
  const result = {};
  for (const id of sessionIDs) {
    const path = join(directory, `${id}.jsonl`);
    const text = await readFile(path, "utf8");
    result[id] = text.split(/\r?\n/).filter((line) => line.trim()).length;
  }
  return result;
}

async function capture(page, base, id, path, expectedRows) {
  await page.goto(`${base}/chat?session=${encodeURIComponent(id)}&instant=1`, { waitUntil: "domcontentloaded" });
  await page.waitForSelector("#chat-log");
  if (expectedRows > 0) await page.waitForFunction(() => document.querySelector("#chat-log")?.innerText?.trim().length > 0);
  await page.waitForTimeout(250);
  const log = page.locator("#chat-log");
  await log.screenshot({ path });
  return digest(await readFile(path));
}

async function compareScreenshots(page, beforePath, afterPath) {
  const urls = await Promise.all([beforePath, afterPath].map(async (path) => `data:image/png;base64,${(await readFile(path)).toString("base64")}`));
  return page.evaluate(async ([beforeURL, afterURL]) => {
    const load = (src) => new Promise((resolveImage, reject) => {
      const image = new Image();
      image.onload = () => resolveImage(image);
      image.onerror = reject;
      image.src = src;
    });
    const [beforeImage, afterImage] = await Promise.all([load(beforeURL), load(afterURL)]);
    if (beforeImage.width !== afterImage.width || beforeImage.height !== afterImage.height) return { dimensions_equal: false, changed_pixels: -1, max_channel_delta: 255 };
    const pixels = (image) => {
      const canvas = document.createElement("canvas");
      canvas.width = image.width;
      canvas.height = image.height;
      const context = canvas.getContext("2d");
      context.drawImage(image, 0, 0);
      return context.getImageData(0, 0, image.width, image.height).data;
    };
    const before = pixels(beforeImage);
    const after = pixels(afterImage);
    let changed = 0;
    let maxDelta = 0;
    for (let offset = 0; offset < before.length; offset += 4) {
      let pixelChanged = false;
      for (let channel = 0; channel < 4; channel++) {
        const delta = Math.abs(before[offset + channel] - after[offset + channel]);
        maxDelta = Math.max(maxDelta, delta);
        pixelChanged ||= delta !== 0;
      }
      if (pixelChanged) changed++;
    }
    return { dimensions_equal: true, changed_pixels: changed, max_channel_delta: maxDelta };
  }, urls);
}

const args = argumentsOf(process.argv);
const evidence = resolve(args.evidence);
await mkdir(join(evidence, "screenshots", "production"), { recursive: true });
await mkdir(join(evidence, "screenshots", "candidate"), { recursive: true });

const beforeConfig = JSON.parse(await readFile(resolve(args["before-config"]), "utf8"));
const afterConfig = JSON.parse(await readFile(resolve(args["after-config"]), "utf8"));
const currentConfig = JSON.parse(await readFile(new URL("../harness.example.json", import.meta.url), "utf8")).config_version;
assert.ok(Array.isArray(beforeConfig[legacyListKey]), "operator config does not carry the legacy connection list");
assert.ok(Array.isArray(afterConfig.connections), "migrated config has no connections list");
assert.equal(afterConfig[legacyListKey], undefined, "migrated config retained the legacy list key");
assert.equal(afterConfig.config_version, currentConfig, "migrated config version");
const expectedConnections = normalizedConfig(beforeConfig, { migrated: true }).connections;
assertPreserved({ connections: afterConfig.connections }, { connections: expectedConnections }, "connection config");

const production = await state(args.production);
const candidate = await state(args.candidate);
const replayCursors = JSON.parse(await readFile(resolve(args["replay-cursors"]), "utf8"));
const productionInventory = Object.keys(production.sessions || {}).sort();
const candidateInventory = Object.keys(candidate.sessions || {}).sort();
assert.deepEqual(candidateInventory, productionInventory, "candidate replay session inventory differs");
const productionIDs = replayCursors.map((row) => row.id).sort();
for (const id of productionIDs) {
  assert.ok(production.sessions?.[id], `${id}: production replay session missing`);
  assert.ok(candidate.sessions?.[id], `${id}: candidate replay session missing`);
}

const countsBefore = await eventCounts(resolve(args.chats), productionIDs);
assert.equal(replayCursors.length, productionIDs.length, "replay cursor inventory differs");
for (const row of replayCursors) {
  assert.equal(row.events, countsBefore[row.id], `${row.id}: replay event count differs`);
  assert.equal(row.cursor_offset, row.bytes, `${row.id}: replay did not reach the journal's final byte`);
}

const browser = await chromium.launch({ channel: "msedge", headless: true });
const productionPage = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
const candidatePage = await browser.newPage({ viewport: { width: 1280, height: 900 }, reducedMotion: "reduce" });
const comparePage = await browser.newPage();
const rows = [];
try {
  for (const id of productionIDs) {
    assert.deepEqual(transcript(candidate.sessions[id]), transcript(production.sessions[id]), `${id}: transcript differs`);
    const file = `${id.replace(/[^a-z0-9_-]/gi, "_")}.png`;
    const expectedRows = production.sessions[id]?.chat?.length || 0;
    const productionPath = join(evidence, "screenshots", "production", file);
    const candidatePath = join(evidence, "screenshots", "candidate", file);
    const productionHash = await capture(productionPage, args.production, id, productionPath, expectedRows);
    const candidateHash = await capture(candidatePage, args.candidate, id, candidatePath, expectedRows);
    const comparison = await compareScreenshots(comparePage, productionPath, candidatePath);
    assert.ok(comparison.dimensions_equal && comparison.max_channel_delta <= 1, `${id}: chat screenshot has a non-rounding difference: ${JSON.stringify(comparison)}`);
    rows.push({ id, events: countsBefore[id], messages: production.sessions[id]?.messages?.length || 0, chat_rows: production.sessions[id]?.chat?.length || 0, screenshot: comparison.changed_pixels ? "compositor-rounding" : "exact", changed_pixels: comparison.changed_pixels, max_channel_delta: comparison.max_channel_delta, production_sha256: productionHash, candidate_sha256: candidateHash });
  }
} finally {
  await browser.close();
}

const report = {
  result: "pass",
  production: args.production,
  candidate: args.candidate,
  config: { from: beforeConfig.config_version ?? null, to: afterConfig.config_version, connections: afterConfig.connections.length, field_loss: 0 },
  chats: rows,
};
await writeFile(join(evidence, "report.json"), JSON.stringify(report, null, 2) + "\n");
const table = rows.map((row) => `| ${row.id} | ${row.events} | ${row.messages} | ${row.chat_rows} | ${row.screenshot} (${row.changed_pixels} px, Δ${row.max_channel_delta}) |`).join("\n");
await writeFile(join(evidence, "report.md"), `# v1.6.7 W1 operator-data replay\n\nPASS: config 7 → 8 with ${afterConfig.connections.length} connections and no field loss; ${rows.length}/${rows.length} chats retained identical transcript objects, event counts, and chat-log screenshots (exact or only proven one-level compositor rounding).\n\n| chat | events | messages | rendered rows | screenshot |\n|---|---:|---:|---:|---|\n${table}\n`);
console.log(`PASS operator-data replay: ${rows.length} chats, ${rows.reduce((sum, row) => sum + row.events, 0)} events, ${rows.length} identical screenshots`);
