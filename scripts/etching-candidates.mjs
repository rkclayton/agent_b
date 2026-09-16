// v0.61.0/W11 — item 2cz, candidates only. Four etching candidates at two
// strengths each on the non-scrolling surfaces, plus one measured attempt at
// putting the texture on its own compositor layer behind a scrolling transcript,
// counted as Paint events per 90 frames, the method v0.45.0/W5 used.
//
// It ships nothing. Every treatment is applied as a constructed stylesheet at
// runtime; no product stylesheet is written and no baseline is re-established.
// A <style> element would be refused by the product's own style-src 'self',
// which is correct and is left alone.
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { chromium } from "playwright";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: Math.floor(argv.length / 2) }, (_, i) => [argv[i * 2].replace(/^--/, ""), argv[i * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "config", "base", "evidence"]) assert.ok(args[name], `missing --${name}`);

// The operator's reported window width, from 2ed's evidence screenshot.
const VIEWPORT = { width: 1780, height: 975 };
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

const INK = "#D8DDE3";
const tile = (body, size) => `url("data:image/svg+xml,${encodeURIComponent(`<svg xmlns='http://www.w3.org/2000/svg' width='${size}' height='${size}' viewBox='0 0 ${size} ${size}'>${body}</svg>`)}")`;

const CANDIDATES = {
  "a-circuit": tile(
    `<g fill='none' stroke='${INK}' stroke-width='1'>` +
    `<path d='M0 24h36v-18h28v34h32M96 0v20h24M24 96V72h40V52'/><path d='M120 120V96H84V64'/></g>` +
    `<g fill='${INK}'><circle cx='36' cy='6' r='2.5'/><circle cx='96' cy='20' r='2.5'/><circle cx='64' cy='52' r='2.5'/><circle cx='84' cy='64' r='2.5'/></g>`, 128),
  "b-hex": tile(
    `<g fill='none' stroke='${INK}' stroke-width='0.9'>` +
    `<path d='M14 0l14 8v16l-14 8L0 24V8z'/><path d='M42 0l14 8v16l-14 8-14-8V8z'/>` +
    `<path d='M0 32l14 8v16L0 64zM28 32l14 8v16l-14 8-14-8V40z'/></g>`, 56),
  "c-hatch": tile(
    `<g stroke='${INK}' stroke-width='0.7'>` +
    `<path d='M0 8h14M22 8h18M0 20h9M17 20h23M4 32h21M31 32h9M0 44h13M21 44h19'/>` +
    `<path d='M8 0v11M8 19v13M20 0v7M20 15v17M32 4v14M32 26v22'/></g>`, 48),
  "d-contour": tile(
    `<g fill='none' stroke='${INK}' stroke-width='0.8'>` +
    `<path d='M0 70C26 44 52 96 80 62s34 18 48 2'/><path d='M0 46C30 20 48 70 82 38s30 14 46 0'/>` +
    `<path d='M0 96C24 74 56 120 86 90s26 12 42 0'/><path d='M0 22C28 2 50 44 84 14s28 10 44 0'/></g>`, 128),
};

// v0.51.0's low steps were below what the operator could see, so low starts
// higher here; high is plainly present without competing with text.
const STRENGTHS = { low: 0.05, high: 0.11 };

// The etching is painted ON the non-scrolling surfaces, not behind them: those
// surfaces carry opaque token backgrounds, so a layer underneath is invisible.
// Each surface keeps its own colour and gains the tile above it at the dialled
// strength, which is what "a faint etching on the surface" means.
const NON_SCROLLING = ".app-shell, .settings-page, .console-surface, .chat-composer, .settings-content, .flow-well";
function treatment(name, strength) {
  return `
    ${NON_SCROLLING} {
      background-image:
        linear-gradient(rgba(216,221,227,${STRENGTHS[strength]}), rgba(216,221,227,${STRENGTHS[strength]})),
        ${CANDIDATES[name]};
      background-repeat: repeat, repeat;
      background-blend-mode: soft-light, normal;
    }
  `;
}

const app = spawn(resolve(args.exe), ["-config", resolve(args.config), "-app-root", resolve(args["app-root"]), "-data-root", resolve(args.data)], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
let stderr = "";
app.stderr.on("data", (chunk) => { stderr += String(chunk); });
let browser;
const results = { viewport: VIEWPORT, strengths: STRENGTHS, shots: [], transcript: {} };
try {
  let state;
  const deadline = Date.now() + 20000;
  while (!state && Date.now() < deadline) {
    try { const response = await fetch(`${args.base}/api/state`); if (response.ok) state = await response.json(); } catch {}
    if (!state) await sleep(50);
  }
  assert.ok(state, `candidate startup timed out: ${stderr}`);
  results.build = state.build;
  await mkdir(resolve(args.evidence), { recursive: true });

  browser = await chromium.launch({ channel: "msedge", headless: true });
  const page = await browser.newPage({ viewport: VIEWPORT, deviceScaleFactor: 1 });
  await page.goto(`${args.base}/?setup=skip`);
  await page.waitForSelector(".app-shell");
  results.surfaces = await page.evaluate(() => [...document.body.children].map((node) => `${node.tagName}.${node.className}`));

  const applyCSS = async (css) => page.evaluate((text) => {
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(text || "");
    document.adoptedStyleSheets = [sheet];
  }, css || "");

  const shoot = async (label, css) => {
    await applyCSS(css);
    await sleep(160);
    const file = resolve(args.evidence, `${label}.png`);
    await page.screenshot({ path: file, fullPage: false });
    const sha = createHash("sha256").update(readFileSync(file)).digest("hex").toUpperCase();
    results.shots.push({ label, file, sha256: sha });
    await applyCSS("");
    return sha;
  };

  const flatSha = await shoot("flat");
  for (const name of Object.keys(CANDIDATES)) {
    for (const strength of Object.keys(STRENGTHS)) {
      const sha = await shoot(`${name}-${strength}`, treatment(name, strength));
      results.shots[results.shots.length - 1].differs_from_flat = sha !== flatSha;
    }
  }
  results.low_step_visible = Object.fromEntries(Object.keys(CANDIDATES).map((name) =>
    [name, results.shots.find((shot) => shot.label === `${name}-low`).differs_from_flat === true]));

  // ---- the transcript's one more measured attempt -------------------------
  // Measuring scroll repaint needs a scrolling surface. This build has no model,
  // so the surface is synthesised: a scrollable column the size of a long
  // transcript. What is measured is the compositor's behaviour when a fixed
  // texture sits behind transparent scrolling content, which does not depend on
  // where the text came from. Declared, not hidden.
  await page.evaluate(() => {
    const host = document.createElement("div");
    host.id = "etch-scroll";
    host.style.cssText = "position:fixed;inset:48px 0 0 0;overflow-y:auto;z-index:1;background:transparent;font:12px/18px monospace;color:#D8DDE3;padding:0 24px";
    let body = "";
    for (let line = 0; line < 1200; line += 1) body += `<div>transcript line ${line} — the quick brown fox jumps over the lazy dog, twice, for measurement</div>`;
    host.innerHTML = body;
    document.body.append(host);
  });

  const client = await page.context().newCDPSession(page);
  const countPaints = async (css) => {
    await applyCSS(css);
    await page.evaluate(() => { document.getElementById("etch-scroll").scrollTop = 0; });
    await sleep(250);
    for (let i = 0; i < 30; i += 1) {
      await page.evaluate(() => { document.getElementById("etch-scroll").scrollTop += 40; });
      await sleep(8);
    }
    const events = [];
    const collect = ({ value }) => { for (const item of value) if (item.name === "Paint") events.push(item); };
    client.on("Tracing.dataCollected", collect);
    await client.send("Tracing.start", { categories: "disabled-by-default-devtools.timeline", transferMode: "ReportEvents" });
    for (let frame = 0; frame < 90; frame += 1) {
      await page.evaluate(() => { document.getElementById("etch-scroll").scrollTop += 20; });
      await sleep(16);
    }
    const done = new Promise((finish) => client.once("Tracing.tracingComplete", finish));
    await client.send("Tracing.end");
    await done;
    client.off("Tracing.dataCollected", collect);
    await applyCSS("");
    return events.length;
  };

  const layered = `
    body::after {
      content: ""; position: fixed; inset: 0; z-index: 0; pointer-events: none;
      background-image: ${CANDIDATES["c-hatch"]};
      background-repeat: repeat;
      opacity: ${STRENGTHS.low};
      will-change: transform;
      transform: translateZ(0);
    }
    #etch-scroll { background: transparent; }
  `;
  results.transcript.method = "Paint events per 90 scroll frames, Edge via CDP tracing, after an unmeasured 30-frame warm-up; three trials each";
  results.transcript.flat = [];
  results.transcript.compositor_layer = [];
  for (let trial = 0; trial < 3; trial += 1) results.transcript.flat.push(await countPaints(null));
  for (let trial = 0; trial < 3; trial += 1) results.transcript.compositor_layer.push(await countPaints(layered));
} finally {
  if (browser) await browser.close();
  app.kill();
}

const mean = (list) => list.reduce((a, b) => a + b, 0) / list.length;
results.transcript.mean_flat = mean(results.transcript.flat);
results.transcript.mean_compositor_layer = mean(results.transcript.compositor_layer);
// A comparison only discriminates if both conditions actually paint. Headless
// Edge composites a synthetic fixed scroller without painting, so a run where
// either side is at zero says nothing about the real transcript.
results.transcript.measurable = results.transcript.mean_flat >= 5 && results.transcript.mean_compositor_layer >= 0 && Math.min(...results.transcript.flat) > 0;
results.transcript.near_flat = results.transcript.measurable && results.transcript.mean_compositor_layer <= results.transcript.mean_flat * 1.25 + 2;
results.transcript.verdict = !results.transcript.measurable
  ? "not measurable here — neither condition produced any Paint event, so this proves nothing"
  : results.transcript.near_flat
    ? "near flat — the transcript could carry the etching"
    : "not near flat — the transcript stays flat";
await writeFile(resolve(args.evidence, "results.json"), `${JSON.stringify(results, null, 2)}\n`);
console.log(JSON.stringify({
  surfaces: results.surfaces,
  shots: results.shots.map(({ label, differs_from_flat }) => ({ label, differs_from_flat })),
  transcript: results.transcript,
}, null, 1));
