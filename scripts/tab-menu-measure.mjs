// Item 2gh, v1.1.2/W5 — "the menu when you right click feels a bit janky
// honestly" turned into numbers. Every property the item lists is measured as
// the operator meets it, before and after the fixes, with a capture beside it.
//
//   node scripts/tab-menu-measure.mjs --exe <exe> --app-root <dir> --data <dir> \
//     --evidence <dir> --label before
import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { start } from "./ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const label = args.label || "before";
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });

const menuSelector = ".agent-tab-wrap.selected .agent-chat-menu";
const tabSelector = '.agent-tab-wrap.selected .agent-tab';

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: true });
const measured = {};
try {
  const sessionID = Object.keys(harness.initial.sessions)[0];
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat?session=${sessionID}`);
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await page.waitForTimeout(500);

  const tab = page.locator(tabSelector).first();
  const box = await tab.boundingBox();
  // A right-click well inside the tab but away from its centre, so "at the
  // pointer" and "at the anchor" cannot be confused. The click is made
  // element-relative, which is how the acceptance suite drives this menu; a
  // bare mouse click at the same coordinates lands on the close mark that
  // overlays the tab's trailing edge and opens nothing.
  const offset = { x: Math.round(box.width * 0.35), y: Math.round(box.height - 4) };
  const pointer = { x: Math.round(box.x + offset.x), y: Math.round(box.y + offset.y) };
  const openMenu = async () => {
    await tab.click({ button: "right", position: offset });
    await page.locator(menuSelector).waitFor({ state: "visible", timeout: 8000 }).catch(() => {});
  };
  const started = Date.now();
  await openMenu();
  // Wall-clock from the click to the menu being visible, driven from outside
  // the page, so it includes anything the page does before it paints.
  measured.open_delay_ms = Date.now() - started;
  measured.opened = await page.locator(menuSelector).isVisible().catch(() => false);

  measured.position = await page.evaluate(({ selector, pointer }) => {
    const menu = document.querySelector(selector);
    if (!menu || menu.hidden) return null;
    const rect = menu.getBoundingClientRect();
    const style = getComputedStyle(menu);
    return {
      pointer,
      menu: { left: Math.round(rect.left), top: Math.round(rect.top), width: Math.round(rect.width), height: Math.round(rect.height) },
      dx_from_pointer: Math.round(rect.left - pointer.x),
      dy_from_pointer: Math.round(rect.top - pointer.y),
      inside_window: rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight,
      animation: style.animationName === "none" ? null : style.animationName,
      transition: style.transitionProperty === "all" || style.transitionProperty === "none" ? style.transitionProperty : style.transitionProperty,
      opacity: style.opacity,
    };
  }, { selector: menuSelector, pointer });
  await page.screenshot({ path: join(evidence, `w5-tab-menu-${label}.png`) });

  // Entry geometry and states: hit targets and whether hover/active are drawn.
  measured.entries = await page.evaluate(({ selector }) => {
    const menu = document.querySelector(selector);
    if (!menu) return null;
    const rows = [...menu.querySelectorAll("button, a")];
    return {
      count: rows.length,
      labels: rows.map((row) => (row.textContent || "").trim()).slice(0, 12),
      min_height: rows.length ? Math.min(...rows.map((row) => Math.round(row.getBoundingClientRect().height))) : null,
      max_height: rows.length ? Math.max(...rows.map((row) => Math.round(row.getBoundingClientRect().height))) : null,
      focusable: rows.filter((row) => row.tabIndex >= 0).length,
    };
  }, { selector: menuSelector });

  // Does the menu shift as its content loads? Sampled over a second.
  measured.load_shift = await page.evaluate(async ({ selector }) => {
    const samples = [];
    for (let index = 0; index < 8; index++) {
      const rect = document.querySelector(selector)?.getBoundingClientRect();
      samples.push(rect ? { top: Math.round(rect.top), height: Math.round(rect.height) } : null);
      await new Promise((done) => setTimeout(done, 120));
    }
    const heights = new Set(samples.filter(Boolean).map((sample) => sample.height));
    const tops = new Set(samples.filter(Boolean).map((sample) => sample.top));
    return { distinct_heights: heights.size, distinct_tops: tops.size, samples };
  }, { selector: menuSelector });

  // Keyboard: does Escape close it, and do arrow keys move between entries?
  await page.keyboard.press("ArrowDown");
  measured.keyboard_arrow_focus = await page.evaluate(({ selector }) => {
    const menu = document.querySelector(selector);
    return !!menu && menu.contains(document.activeElement) ? (document.activeElement.textContent || "").trim() : null;
  }, { selector: menuSelector });
  await page.keyboard.press("Escape");
  await page.waitForTimeout(250);
  measured.escape_dismisses = await page.evaluate(({ selector }) => {
    const menu = document.querySelector(selector);
    return !menu || menu.hidden || menu.getBoundingClientRect().height === 0;
  }, { selector: menuSelector });

  // Outside click, and whether the click that dismisses is swallowed.
  const reopen = openMenu;
  await reopen();
  await page.evaluate(() => { window.__menuProbeClicks = 0; document.getElementById("chat-task").addEventListener("click", () => { window.__menuProbeClicks++; }); });
  const composer = await page.locator("#chat-task").boundingBox();
  await page.mouse.click(Math.round(composer.x + composer.width / 2), Math.round(composer.y + composer.height / 2));
  await page.waitForTimeout(200);
  measured.outside_click_dismisses = await page.evaluate(({ selector }) => {
    const menu = document.querySelector(selector);
    return !menu || menu.hidden || menu.getBoundingClientRect().height === 0;
  }, { selector: menuSelector });
  measured.outside_click_reached_the_page = await page.evaluate(() => window.__menuProbeClicks) > 0;

  // A second right-click on the same tab.
  await reopen();
  // A menu that opens at the pointer covers the pointer, so the second
  // right-click is made to the LEFT of the first, where the tab is still
  // exposed — the menu's left edge is the first pointer's x.
  await tab.click({ button: "right", position: { x: Math.max(2, Math.round(box.width * 0.1)), y: offset.y } });
  await page.waitForTimeout(250);
  measured.second_right_click_dismisses = await page.evaluate(({ selector }) => {
    const menu = document.querySelector(selector);
    return !menu || menu.hidden || menu.getBoundingClientRect().height === 0;
  }, { selector: menuSelector });

  // At the window's edge: the menu must stay inside the window.
  await page.setViewportSize({ width: 620, height: 520 });
  await page.waitForTimeout(400);
  const narrowTab = await page.locator(tabSelector).first().boundingBox();
  if (narrowTab) {
    await page.locator(tabSelector).first().click({ button: "right", position: { x: Math.round(narrowTab.width * 0.35), y: Math.round(narrowTab.height - 4) }, force: true });
    await page.locator(menuSelector).waitFor({ state: "visible", timeout: 5000 }).catch(() => {});
    measured.at_the_edge = await page.evaluate(({ selector }) => {
      const rect = document.querySelector(selector)?.getBoundingClientRect();
      if (!rect) return null;
      return { left: Math.round(rect.left), top: Math.round(rect.top), right: Math.round(rect.right), bottom: Math.round(rect.bottom), window: { width: innerWidth, height: innerHeight }, inside: rect.left >= 0 && rect.top >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight };
    }, { selector: menuSelector });
    await page.screenshot({ path: join(evidence, `w5-tab-menu-edge-${label}.png`) });
  }
  await page.close();
} finally {
  await harness.stop();
}

await writeFile(join(evidence, `w5-tab-menu-${label}.json`), JSON.stringify({ schema: 1, label, measured }, null, 2));
process.stdout.write(`${JSON.stringify({ label, ...measured }, null, 2)}\n`);
// The disposable root goes through the removal guard, never a raw recursive
// rm: the guard refuses a target outside the allowed roots and refuses to
// descend through a junction (scripts/removal-guard.mjs).
try { removeTreeWithinAllowedRoots(resolve(args.data), [tmpdir()], "v1.1.3 disposable scenario root"); } catch { /* a root already gone is not a failure */ }
