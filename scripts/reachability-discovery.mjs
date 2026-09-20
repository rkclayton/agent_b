// Item 2gf, v1.1.2/W1 — DISCOVERY. Reproduce the operator's trap and record
// what the window actually offered him, before anything is changed.
//
//   node scripts/reachability-discovery.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
//
// It answers the item's two [discovery] questions: whether the Plan page mounts
// inside the shell, and what exactly trapped him. It asserts nothing — a
// discovery script that fails on the defect it is measuring measures nothing.
import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { start } from "./ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });

// What a page offers as a way back to the chat, measured in the page rather
// than inferred: does the strip exist, does it have tabs, and does clicking a
// tab actually land on the chat?
async function surveyPage(page) {
  return page.evaluate(() => {
    const visible = (element) => {
      if (!element) return false;
      const style = getComputedStyle(element);
      const box = element.getBoundingClientRect();
      return style.display !== "none" && style.visibility !== "hidden" && box.width > 0 && box.height > 0;
    };
    const shell = document.getElementById("app-shell");
    const tabs = [...document.querySelectorAll(".agent-tab")];
    const planLink = document.querySelector('.shell-page[data-page="plan"]');
    return {
      page: document.body.dataset.page || null,
      shell_present: !!shell,
      shell_visible: visible(shell),
      shell_has_children: !!shell && shell.children.length > 0,
      tab_strip_present: !!document.querySelector(".agent-tabs"),
      tab_strip_visible: visible(document.querySelector(".agent-tabs")),
      tab_count: tabs.length,
      tabs_with_session: tabs.filter((tab) => tab.dataset.session).length,
      new_chat_present: !!document.querySelector(".agent-tab-new"),
      new_chat_visible: visible(document.querySelector(".agent-tab-new")),
      new_chat_disabled: document.querySelector(".agent-tab-new")?.disabled ?? null,
      plan_link_present: !!planLink,
      plan_link_visible: visible(planLink),
      plan_link_aria_current: planLink?.getAttribute("aria-current") ?? null,
      plan_link_href: planLink?.getAttribute("href") ?? null,
      settings_present: !!document.querySelector(".shell-settings"),
      settings_visible: visible(document.querySelector(".shell-settings")),
    };
  });
}

// Click the first chat tab and say where the window ended up. This is the
// operator's actual gesture: "chat should always be accessible".
async function clickTabAndReport(page, base) {
  const before = page.url();
  const tab = page.locator(".agent-tab[data-session]").first();
  if (!(await tab.count())) return { attempted: false, reason: "no chat tab to click", before, after: before, reached_chat: false };
  await tab.click().catch(() => {});
  await page.waitForTimeout(1200);
  const after = page.url();
  const bodyPage = await page.evaluate(() => document.body.dataset.page || null);
  return { attempted: true, before, after, body_page: bodyPage, reached_chat: bodyPage === "chat", navigated: after !== before, base };
}

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: false });
const findings = { schema: 1, model_reachable: false, build: harness.initial.build, pages: {}, notes: [] };
try {
  const sessionID = Object.keys(harness.initial.sessions)[0];
  findings.session_id = sessionID;
  const page = await harness.context.newPage();
  const consoleErrors = [];
  page.on("pageerror", (error) => consoleErrors.push(String(error.message)));
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });

  // 1. The Plan page, reached the way the operator reached it: the Plan toggle
  //    on the chat header.
  await page.goto(`${harness.base}/chat?session=${sessionID}`);
  await page.locator("#chat-task").waitFor({ state: "visible" });
  findings.pages.chat = await surveyPage(page);
  await page.locator('.shell-page[data-page="plan"]').click();
  await page.waitForURL((url) => url.pathname === "/plan", { timeout: 15000 }).catch(() => {});
  await page.waitForTimeout(1500);
  findings.pages.plan = await surveyPage(page);
  await page.screenshot({ path: join(evidence, "w1-plan-trapped.png") });
  findings.plan_tab_click = await clickTabAndReport(page, harness.base);
  await page.screenshot({ path: join(evidence, "w1-plan-after-tab-click.png") });
  findings.plan_toggle_returns = await page.evaluate(() => {
    const link = document.querySelector('.shell-page[data-page="plan"]');
    return { aria_current: link?.getAttribute("aria-current") ?? null, href: link?.getAttribute("href") ?? null, has_click_handler: !!link?.onclick };
  });

  // 2. The same page with /api/plan failing, which is the other half of the
  //    operator's condition.
  await harness.failRoute("**/api/plan*");
  const failing = await harness.context.newPage();
  await failing.goto(`${harness.base}/plan?session=${sessionID}`);
  await failing.waitForTimeout(1500);
  findings.pages.plan_api_failing = await surveyPage(failing);
  findings.plan_api_failing_tab_click = await clickTabAndReport(failing, harness.base);
  await failing.screenshot({ path: join(evidence, "w1-plan-api-failing.png") });
  await failing.close();

  // 3. Console and Settings, for the same question.
  const other = await harness.context.newPage();
  await other.goto(`${harness.base}/?session=${sessionID}`);
  await other.waitForTimeout(1200);
  findings.pages.console = await surveyPage(other);
  findings.console_tab_click = await clickTabAndReport(other, harness.base);
  await other.goto(`${harness.base}/?session=${sessionID}`);
  await other.waitForTimeout(800);
  await other.locator(".shell-settings").click().catch(() => {});
  await other.waitForTimeout(1200);
  findings.pages.settings = await surveyPage(other);
  findings.settings_tab_click = await clickTabAndReport(other, harness.base);
  await other.close();

  // 4. Setup, the one page the strip is not expected on.
  const setup = await harness.context.newPage();
  await setup.goto(`${harness.base}/setup`).catch(() => {});
  await setup.waitForTimeout(1000);
  findings.pages.setup = await surveyPage(setup);
  await setup.screenshot({ path: join(evidence, "w1-setup.png") });
  await setup.close();

  findings.console_errors = consoleErrors;
  await page.close();
} finally {
  await harness.stop();
  findings.stderr = harness.stderr();
}
await writeFile(join(evidence, "w1-2gf-discovery.json"), JSON.stringify(findings, null, 2));
process.stdout.write(`${JSON.stringify({
  plan_mounts_shell: findings.pages.plan?.shell_has_children,
  plan_strip_visible: findings.pages.plan?.tab_strip_visible,
  plan_tab_click_reached_chat: findings.plan_tab_click?.reached_chat,
  plan_toggle_aria_current: findings.plan_toggle_returns?.aria_current,
  console_tab_click_reached_chat: findings.console_tab_click?.reached_chat,
  settings_tab_click_reached_chat: findings.settings_tab_click?.reached_chat,
}, null, 2)}\n`);
// The disposable root goes through the removal guard, never a raw recursive
// rm: the guard refuses a target outside the allowed roots and refuses to
// descend through a junction (scripts/removal-guard.mjs).
try { removeTreeWithinAllowedRoots(resolve(args.data), [tmpdir()], "v1.1.3 disposable scenario root"); } catch { /* a root already gone is not a failure */ }
