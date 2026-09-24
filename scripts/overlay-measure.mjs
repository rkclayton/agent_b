// Item 2gl / 2ck, v1.2.6/W1 — does the window-controls overlay activate?
//
// The operator: "are we still looking at revising the code so we can edit the
// top bar? this is kinda bugging me." Edge draws its own title strip above the
// app, so the window says Agent_b in a system strip and the tab immediately
// below it says agent_b too. The app's own stylesheet has been ready for the
// overlay since v0.61.0 - drag regions, no-drag exemptions, and sizing against
// env(titlebar-area-width). What is missing is Edge agreeing to hand the top
// edge over.
//
// 2ck measured `--app=` and recorded the answer: standalone display mode, but
// overlay visible:false, a zero rectangle, and no overlay display-mode match.
// Microsoft documents the overlay for INSTALLED desktop PWAs, so this measures
// the installed path instead - and reports what it finds either way, because
// the consequence of a no is that this stops being a launch flag and becomes a
// packaging decision the operator makes.
//
//   node scripts/overlay-measure.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { chromium } from "@playwright/test";
import { freePort, start, waitFor } from "./ui-harness.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });

const sleep = (ms) => new Promise((done) => setTimeout(done, ms));

// What the page can see about the overlay, and how tall the chrome above the
// document is. The second number is the one the operator feels.
const overlayState = `(() => {
  const overlay = navigator.windowControlsOverlay;
  const rect = overlay && overlay.getTitlebarAreaRect ? overlay.getTitlebarAreaRect() : null;
  return {
    hasAPI: !!overlay,
    visible: !!(overlay && overlay.visible),
    rect: rect ? { x: rect.x, y: rect.y, width: rect.width, height: rect.height } : null,
    displayModeStandalone: matchMedia("(display-mode: standalone)").matches,
    displayModeOverlay: matchMedia("(display-mode: window-controls-overlay)").matches,
    titlebarAreaHeight: getComputedStyle(document.documentElement).getPropertyValue("--app-header-height").trim(),
    // The chrome above the document: the window's outer height less what the
    // document actually gets.
    outerHeight: window.outerHeight,
    innerHeight: window.innerHeight,
    chromeAbove: window.outerHeight - window.innerHeight,
  };
})()`;

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: true });
const measurements = [];
const children = [];
try {
  const manifest = await (await fetch(`${harness.base}/static/app.webmanifest`)).json();
  assert.equal(manifest.display_override?.[0], "window-controls-overlay", "the manifest must ask for the overlay");

  // Edge is launched DIRECTLY, not through the automation driver: a driver
  // opens its own ordinary window, which is why a first attempt reported
  // standalone:false for a flag that certainly produces standalone. The window
  // is then attached to over the debugging port, so what is measured is the
  // window the operator would actually get.
  const edge = "C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe";
  const connectionRoot = join(resolve(args.data), "edge-connections");
  await mkdir(connectionRoot, { recursive: true });

  async function measure(name, extraArgs, { installFirst = "" } = {}) {
    const port = await freePort();
    const connection = join(connectionRoot, name);
    await mkdir(connection, { recursive: true });
    if (installFirst) {
      // An install, if this build carries the switch. It exits by itself.
      const installer = spawn(edge, [`--user-data-dir=${connection}`, "--no-first-run", installFirst], { windowsHide: true, stdio: "ignore" });
      children.push(installer);
      await Promise.race([new Promise((done) => installer.once("exit", done)), sleep(20000)]);
    }
    const child = spawn(edge, [
      `--user-data-dir=${connection}`,
      "--no-first-run",
      "--no-default-browser-check",
      `--remote-debugging-port=${port}`,
      ...extraArgs,
    ], { windowsHide: false, stdio: "ignore" });
    children.push(child);
    let browser = null;
    try {
      browser = await waitFor(async () => chromium.connectOverCDP(`http://127.0.0.1:${port}`), `${name}: attached to Edge`, 25000);
      const page = await waitFor(async () => {
        const found = browser.contexts().flatMap((context) => context.pages()).find((item) => item.url().includes("/chat"));
        return found || null;
      }, `${name}: found the app window`, 25000);
      await waitFor(async () => page.evaluate(() => !!document.getElementById("app-shell")), `${name}: the shell drew`, 20000);
      await sleep(800);
      const state = await page.evaluate(overlayState);
      measurements.push({ attempt: name, args: extraArgs, installFirst, ...state });
      process.stdout.write(`${name}: overlay visible=${state.visible} rect=${JSON.stringify(state.rect)} chromeAbove=${state.chromeAbove}px standalone=${state.displayModeStandalone} overlayMode=${state.displayModeOverlay}\n`);
    } catch (error) {
      measurements.push({ attempt: name, args: extraArgs, installFirst, error: error.message });
      process.stdout.write(`${name}: FAILED ${error.message}\n`);
    } finally {
      await browser?.close().catch(() => {});
      // Hard stop (12): a process this script started is ended by its PID.
      if (child.exitCode === null) {
        child.kill();
        await Promise.race([new Promise((done) => child.once("exit", done)), sleep(4000)]);
      }
    }
  }

  // 1. An ordinary window, for the chrome height the operator sees today.
  await measure("ordinary-window", [`${harness.base}/chat`]);
  // 2. The path production uses: --app= on the loopback origin.
  await measure("app-flag", [`--app=${harness.base}/chat`]);
  // 3. The installed path Microsoft documents the overlay for.
  await measure("installed-pwa", [`--app=${harness.base}/chat`], { installFirst: `--install-webapp=${harness.base}/chat` });
} finally {
  for (const child of children) {
    if (child.exitCode === null) child.kill();
  }
  await harness.stop();
}

const activated = measurements.some((item) => item.visible && item.rect && item.rect.height > 0);
await writeFile(join(evidence, "overlay-measurement.json"), `${JSON.stringify({ activated, measurements }, null, 1)}\n`);
process.stdout.write(`${activated ? "OVERLAY ACTIVATES" : "OVERLAY DOES NOT ACTIVATE"}\n`);
