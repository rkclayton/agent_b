// v0.61.0/W12 — the pixel half of 2ed: Settings section heights before and
// after, at the operator's window size, in a real browser against a real build.
// "Before" is served by checking the previous web/ tree into a scratch app root
// and pointing a second instance of the same binary at it, so only the page
// modules differ.
import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync, cpSync, rmSync } from "node:fs";
import { resolve, join, dirname } from "node:path";
import { chromium } from "playwright";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: Math.floor(argv.length / 2) }, (_, i) => [argv[i * 2].replace(/^--/, ""), argv[i * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "config", "base", "before-ref", "evidence"]) assert.ok(args[name], `missing --${name}`);

const VIEWPORT = { width: 1780, height: 975 };
const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

function serveTree(root) {
  const app = spawn(resolve(args.exe), ["-config", resolve(args.config), "-app-root", resolve(root), "-data-root", resolve(args.data)], { windowsHide: true, stdio: ["ignore", "ignore", "pipe"] });
  return app;
}

async function measure(root, label) {
  const app = serveTree(root);
  let browser;
  try {
    let ready = false;
    const deadline = Date.now() + 20000;
    while (!ready && Date.now() < deadline) {
      try { const r = await fetch(`${args.base}/api/state`); ready = r.ok; } catch {}
      if (!ready) await sleep(60);
    }
    assert.ok(ready, `${label}: startup timed out`);
    browser = await chromium.launch({ channel: "msedge", headless: true });
    const page = await browser.newPage({ viewport: VIEWPORT, deviceScaleFactor: 1 });
    await page.goto(`${args.base}/?setup=skip`);
    await page.waitForSelector(".app-shell");
    await page.locator(".shell-settings").click();
    await page.waitForSelector(".settings-content", { timeout: 10000 });
    // Settings opens on Connections; the section this pass is about is Security.
    await page.locator('[data-action="settings-section"][data-id="shell"]').click();
    await page.waitForSelector(".settings-subhead", { timeout: 10000 });
    await sleep(500);
    return await page.evaluate(() => {
      const content = document.querySelector(".settings-content");
      const sections = [...document.querySelectorAll(".settings-group")].map((group) => ({
        heading: group.querySelector("h3, h2, .settings-heading")?.textContent?.trim() || "",
        height: Math.round(group.getBoundingClientRect().height),
      }));
      return {
        settings_content_scroll_height: content ? Math.round(content.scrollHeight) : 0,
        visible_notes: document.querySelectorAll(".settings-note").length,
        hover_titles: [...document.querySelectorAll("[title]")].filter((n) => (n.getAttribute("title") || "").length > 12).length,
        controls: document.querySelectorAll("button, input, select, textarea, [role=switch]").length,
        sections,
      };
    });
  } finally {
    if (browser) await browser.close();
    app.kill();
    await sleep(600);
  }
}

const scratch = resolve(args.evidence, "before-app");
removeTreeWithinAllowedRoots(scratch, [args.evidence], "settings-height scratch cleanup");
mkdirSync(scratch, { recursive: true });
const files = execFileSync("git", ["ls-tree", "-r", "--name-only", args["before-ref"], "web/"], { encoding: "utf8" }).split("\n").filter(Boolean);
for (const name of files) {
  const body = execFileSync("git", ["show", `${args["before-ref"]}:${name}`], { encoding: "buffer", maxBuffer: 64 * 1024 * 1024 });
  const target = join(scratch, name);
  mkdirSync(dirname(target), { recursive: true });
  writeFileSync(target, body);
}
cpSync(resolve(args["app-root"], "prompts"), join(scratch, "prompts"), { recursive: true });

const before = await measure(scratch, "before");
const after = await measure(resolve(args["app-root"]), "after");
const report = {
  viewport: VIEWPORT,
  before_ref: args["before-ref"],
  before,
  after,
  settings_shorter: after.settings_content_scroll_height < before.settings_content_scroll_height,
  height_delta_px: after.settings_content_scroll_height - before.settings_content_scroll_height,
  control_delta: after.controls - before.controls,
};
writeFileSync(resolve(args.evidence, "settings-heights.json"), `${JSON.stringify(report, null, 2)}\n`);
removeTreeWithinAllowedRoots(scratch, [args.evidence], "settings-height scratch cleanup");
console.log(JSON.stringify(report, null, 1));
