import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { start } from "../ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "../../tools/removal-guard.mjs";

const run = promisify(execFile);
const hash = async (path) => createHash("sha256").update(await readFile(path)).digest("hex");
const repo = resolve(fileURLToPath(new URL("../..", import.meta.url)));
let root;
let harness;

test.beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "agentb-discovery-playwright-"));
  const exe = join(root, "Agent_b.exe");
  await run(join(repo, ".tools", "go", "bin", "go.exe"), ["build", "-o", exe, "./cmd/harness"], { cwd: repo, windowsHide: true });
  harness = await start({
    exe,
    appRoot: repo,
    data: join(root, "data"),
    modelIDs: ["alpha-model", "beta-model"],
    // Item 2nq: the literal "model" cannot be a saved model any more — a config
    // carrying it is migrated to an empty one at load — so this fixture names a model
    // the server does not serve instead. What it is testing is a SAVED model the
    // listing does not offer, which is still exactly this case.
    connectionModel: "absent-model",
  });
});

test.afterAll(async () => {
  await harness?.stop();
  if (root) removeTreeWithinAllowedRoots(root, [tmpdir()], "connection-discovery Playwright cleanup");
});

// NOTE, measured at rel-1.31.0: every case here opens its own page, and a page holds
// an SSE stream. Six of them reach the browser's per-origin HTTP/1.1 connection limit
// and the next page's requests queue behind them forever — which showed up as a save
// that said "Saving changes…" and never finished. Each case closes its page.
test("Setup and Connections share endpoint discovery and the model picker", async () => {
  const setup = await harness.context.newPage();
  await setup.goto(`${harness.base}/setup`);
  await setup.getByRole("button", { name: "Test" }).click();
  await expect(setup.locator(".discovery-note")).toHaveText(`found http://127.0.0.1:${harness.modelPort}`);
  await expect(setup.locator('[data-field="model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(setup.locator('[data-field="model"] option')).toHaveText(["absent-model · not served", "alpha-model", "beta-model"]);
  await expect(setup.locator(".setup-feedback")).toContainText('Model "absent-model" is not served');

  const settings = await harness.context.newPage();
  await settings.goto(`${harness.base}/chat?from=setup#settings/connections`);
  await settings.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  const before = await hash(join(harness.dataRoot, "harness.json"));
  // Item 2l5: Test is one of the four actions on the connection own row now.
  await settings.locator('.connection-editor [data-action="probe"][data-id="ui"]').click();
  // Item 2nb (g): ONE MESSAGE, ONE PLACE. The result of the last Test is rendered
  // under the field it is about and nowhere else. The operator saw three copies of one
  // sentence before this; the note carries the message when there is one, and what
  // discovery found when there is not.
  // 2po replaces the old full model-refusal sentence with the planner's exact
  // fallback sentence; its unchanged full wording is one click away.
  await expect(settings.locator(".connection-editor .connection-test-failure")).toHaveCount(1);
  await expect(settings.locator(".connection-editor .connection-test-failure")).toContainText("The test failed.");
  await settings.locator('.connection-editor [data-action="error-details"]').click();
  await expect(settings.locator(".settings-error-panel pre")).toContainText('Model "absent-model" is not served');
  await settings.keyboard.press("Escape");
  // (c): the model control is a dropdown ALWAYS, and it always offers the one way out
  // for a server that cannot list its models.
  await expect(settings.locator('[data-path="connections.ui.model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(settings.locator('[data-path="connections.ui.model"] option')).toHaveText(["absent-model", "alpha-model", "beta-model", "type a name…"]);
  // (g) again, from the other side: the row header carries the STATE WORD and never
  // the message, so it cannot overflow.
  const header = settings.locator('.connection-row:has([data-action="connection-toggle"][data-id="ui"]) .connection-state');
  await expect(header).not.toContainText('is not served');
  await expect(header).not.toHaveText("");
  expect(await hash(join(harness.dataRoot, "harness.json"))).toBe(before);
  await setup.close();
  await settings.close();
});

// Item 2mh (a) and (c): THE SWITCHER, MEASURED IN THE RUNNING APP.
//
// "the model switcher in chat only displays like 10 characters. we need the text for
// models much smaller so it can fully fit long names - i dont want elipsis." The
// @verify asked for the label column's REAL rendered width, because the 90px floor
// was read from a rule and the row might have been getting less for another reason.
// This opens the menu and measures.
test("the switcher's model column has real room and no ellipsis", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();
  await page.locator(".shell-session-title").click();
  const row = page.locator(".shell-connection-choice").first();
  await expect(row).toBeVisible();
  const measured = await row.evaluate((node) => {
    const model = node.querySelector(".shell-connection-model");
    const style = getComputedStyle(model);
    return {
      menu: node.closest(".shell-menu").getBoundingClientRect().width,
      model: model.getBoundingClientRect().width,
      overflow: style.textOverflow,
      fontSize: style.fontSize,
      clipped: model.scrollWidth > model.clientWidth + 1,
    };
  });
  // The column the operator was getting 90px of. It is a floor of 180 and a 2fr
  // share now, and the menu it sits in is 680px wide.
  expect(measured.model, JSON.stringify(measured)).toBeGreaterThanOrEqual(180);
  expect(measured.overflow, JSON.stringify(measured)).toBe("clip");
  expect(measured.clipped, "the model name is being cut off").toBe(false);
  expect(Number.parseFloat(measured.fontSize)).toBeLessThan(12);
  await page.close();
});

test("the active chat tab returns from Plan and three Settings depths", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await expect(page.locator("#chat-log")).toBeVisible();
	const session = await page.locator(".agent-tab[data-session]").first().getAttribute("data-session");

	await page.goto(`${harness.base}/plan?session=${encodeURIComponent(session)}`);
	await page.locator(`.agent-tab[data-session="${session}"]`).click();
	await expect(page).toHaveURL(/\/chat/);
	await expect(page.locator("#chat-log")).toBeVisible();

	for (const section of ["connections", "profiles", "shell"]) {
		await page.locator(".shell-settings").click();
		await expect(page.locator("#settings-page")).toBeVisible();
		await page.locator(`.settings-nav [data-id="${section}"]`).click();
		await page.locator(`.agent-tab[data-session="${session}"]`).click();
		await expect(page.locator("#settings-page")).toBeHidden();
		await expect(page.locator("#chat-log")).toBeVisible();
	}

	for (const save of [false, true]) {
		await page.locator(".shell-settings").click();
		await page.locator('.settings-nav [data-id="connections"]').click();
		if (!await page.locator(".connection-editor").count()) await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
		await expect(page.locator(".connection-editor")).toBeVisible();
		await page.locator('.connection-editor [data-path$=".label"]').fill(`UI ${save ? "saved" : "discarded"}`);
		page.once("dialog", (dialog) => save ? dialog.accept() : dialog.dismiss());
		await page.locator(`.agent-tab[data-session="${session}"]`).click();
		await expect(page.locator("#settings-page")).toBeHidden();
		await expect(page.locator("#chat-log")).toBeVisible();
	}
	await page.close();
});

test("Security shows one Phone section and retired browser phone routes are absent", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await page.locator(".shell-settings").click();
	await page.locator('.settings-nav [data-id="shell"]').click();
	const phoneHeading = page.locator(".settings-subhead", { hasText: /^Phone$/ });
	await expect(phoneHeading).toHaveCount(1);
	await expect(page.getByText("Phone access", { exact: true })).toHaveCount(0);
	await expect(page.getByText("Phone away from home", { exact: true })).toHaveCount(0);
	await expect(page.locator('[data-action="broker-pair"], [data-action="broker-cancel"]')).toHaveCount(1);
	for (const path of ["/phone", "/phone-sw.js", "/api/phone/enrolment", "/api/phone/enrolment/redeem", "/api/phone/devices", "/api/phone/push"]) {
		const status = await page.evaluate(async (value) => (await fetch(value, { method: "GET" })).status, path);
		expect(status, path).toBe(404);
	}
	await page.close();
});

test("a failed connection Test stays one line and opens exact details in this window 2po", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat#settings/connections`);
  await expect(page.locator("#settings-page")).toBeVisible();
  await expect(page.locator(".field-error, .connection-refusal, .connection-test-failure")).toHaveCount(0);
  await page.locator('.connection-row [data-action="duplicate-connection"][data-id="ui"]').click();
  const copyPath = await page.locator('.connection-editor [data-path$=".base_url"]').getAttribute("data-path");
  const copyID = copyPath.split(".")[1];
  await page.locator(`[data-path="connections.${copyID}.base_url"]`).fill("http://127.0.0.1:1");
  await page.locator(`[data-action="connection-toggle"][data-id="${copyID}"]`).click();
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await page.locator('[data-path="connections.ui.base_url"]').fill("http://127.0.0.1:2");
  const evidence = process.env.AGENTB_EVIDENCE_DIR;
  if (evidence) {
    await mkdir(evidence, { recursive: true });
    await page.screenshot({ path: join(evidence, "settings-connection-before.png") });
  }
  const timeoutDetail = "Get http://127.0.0.1:2/v1/models: context deadline exceeded while awaiting headers.";
  await page.route("**/api/connections/ui/probe", (route) => route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: timeoutDetail }) }));
  await page.locator('.connection-editor [data-action="probe"][data-id="ui"]').click();
  await expect(page.locator('.connection-editor .connection-test-failure')).toContainText("No answer from 127.0.0.1:2. It may be off, asleep or out of reach of this PC.");
  // Item 2px (a): Test is in the form, so the copy is tested from its own editor;
  // the first connection's failure stays on its collapsed row.
  await page.locator(`[data-action="connection-toggle"][data-id="${copyID}"]`).click();
  const refusedAnswer = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${copyID}/probe`));
  await page.locator(`.connection-editor [data-action="probe"][data-id="${copyID}"]`).click();
  await refusedAnswer;
  await expect(page.locator(".connection-editor .connection-test-failure")).toContainText("Nothing is listening at 127.0.0.1:1.");
  await expect(page.locator(".connection-test-failure")).toHaveCount(2);
  if (evidence) await page.screenshot({ path: join(evidence, "settings-connection-after.png") });
  const pages = harness.context.pages().length;
  await page.locator('.connection-row:has([data-action="connection-toggle"][data-id="ui"]) [data-action="error-details"]').click();
  await expect(page.locator(".settings-error-panel pre")).toHaveText(timeoutDetail);
  expect(harness.context.pages().length, "details opened another window").toBe(pages);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1), "details added horizontal scrolling").toBe(false);
  await page.keyboard.press("Escape");
  await expect(page.locator(".settings-error-panel")).toHaveCount(0);
  await expect(page.locator('.connection-row:has([data-action="connection-toggle"][data-id="ui"]) [data-action="error-details"]')).toHaveCount(1);
  await page.evaluate(async (id) => { await fetch(`/api/connections/${encodeURIComponent(id)}`, { method: "DELETE" }); }, copyID);
  await page.close();
});

test("a refused connection Test keeps today's exact full text 2po", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat#settings/connections`);
  if (!await page.locator('.connection-editor [data-path="connections.ui.base_url"]').count()) await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await page.locator('[data-path="connections.ui.base_url"]').fill("http://127.0.0.1:1");
  const answer = page.waitForResponse((response) => response.url().endsWith("/api/connections/ui/probe"));
  await page.locator('.connection-editor [data-action="probe"][data-id="ui"]').click();
  const response = await answer;
  const body = await response.json();
  await expect(page.locator(".connection-test-failure")).toHaveCount(1);
  await expect(page.locator(".connection-test-failure")).toContainText("Nothing is listening at 127.0.0.1:1.");
  await expect(page.locator('.connection-editor [data-action="error-details"]')).toHaveCount(1);
  await expect(page.locator('.connection-editor .field-error')).toHaveCount(0);
  await page.locator('.connection-editor [data-action="error-details"]').click();
  await expect(page.locator(".settings-error-panel pre")).toHaveText(body.error);
  await page.keyboard.press("Escape");
  await expect(page.locator(".settings-error-panel")).toHaveCount(0);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await expect(page.locator('.connection-row [data-action="error-details"]')).toHaveCount(1);
  await expect(page.locator(".connection-test-failure")).toHaveCount(1);
  await expect(page.locator(".connection-test-failure")).toContainText("Nothing is listening at 127.0.0.1:1.");
  await page.close();
});

test("a refused saved value marks that field only 2po", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat#settings/connections`);
  if (!await page.locator('.connection-editor [data-path="connections.ui.base_url"]').count()) await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await page.route("**/api/config", (route) => route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "That address is refused after Save.", field: "connections.ui.base_url" }) }));
  await page.locator('[data-path="connections.ui.base_url"]').fill("http://127.0.0.1:2");
  await page.locator('.connection-editor [data-action="save-connection"]').click();
  await expect(page.locator('.connection-editor [data-path="connections.ui.base_url"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).toHaveClass(/invalid/);
  await expect(page.locator(".connection-editor .field-error")).toHaveCount(1);
  await expect(page.locator('.connection-editor [data-path$=".label"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).not.toHaveClass(/invalid/);
  await expect(page.locator('.connection-editor [data-path$=".api_key"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).not.toHaveClass(/invalid/);
  await page.close();
});

// Item 2ms: CLOSING THE SELECTED CHAT MOVES THE SELECTION OFF IT.
//
// "when no chat tabs are open its showing me an old chat still in the window."
// Reproduced at rel-1.31.0 against a copy of the operator's own restored journal set,
// 34 chats: closing the one open chat left the selection on it, and close-is-not-
// delete keeps the whole transcript in the store, so the pane went on drawing a chat
// he had just closed - and a reload drew it again.
test("closing the selected chat shows its neighbour, and the last one shows the empty state", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();
  const tabs = () => page.locator(".agent-tab-wrap[data-session]");
  await expect(tabs()).toHaveCount(1);

  // A neighbour to move to.
  await page.locator(".agent-tab-new").click();
  await expect(tabs()).toHaveCount(2);
  const selected = await page.locator(".agent-tab-wrap.selected").getAttribute("data-session");
  const neighbour = await page.locator(`.agent-tab-wrap[data-session]:not([data-session="${selected}"])`).getAttribute("data-session");

  // (a): the neighbour that took its place is selected, and it is SHOWN.
  await page.locator(`.agent-tab-wrap[data-session="${selected}"] .agent-tab-close`).click();
  await expect(tabs()).toHaveCount(1);
  await expect(page.locator(".agent-tab-wrap.selected")).toHaveAttribute("data-session", neighbour);
  await expect(page.locator("#chat-task")).toBeEnabled();

  // (b): closing a chat that is NOT selected moves nothing.
  await page.locator(".agent-tab-new").click();
  await expect(tabs()).toHaveCount(2);
  const stays = await page.locator(".agent-tab-wrap.selected").getAttribute("data-session");
  const other = await page.locator(`.agent-tab-wrap[data-session]:not([data-session="${stays}"])`).getAttribute("data-session");
  await page.locator(`.agent-tab-wrap[data-session="${other}"] .agent-tab-close`).click();
  await expect(tabs()).toHaveCount(1);
  await expect(page.locator(".agent-tab-wrap.selected")).toHaveAttribute("data-session", stays);

  // (a) with nothing to move to, and (d): the empty state is a real state - one
  // line, no tab lit, and no composer for a chat that does not exist.
  await page.locator(`.agent-tab-wrap[data-session="${stays}"] .agent-tab-close`).click();
  await expect(tabs()).toHaveCount(0);
  await expect(page.locator(".agent-tab-wrap.selected")).toHaveCount(0);
  await expect(page.locator(".chat-empty")).toBeVisible();
  await expect(page.locator("#chat-task")).toBeDisabled();

  // (e): and a reload lands in the same place, not back on the last transcript.
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.locator(".chat-empty")).toBeVisible();
  await expect(tabs()).toHaveCount(0);
  await expect(page.locator("#chat-task")).toBeDisabled();

  // This harness is shared with the cases below and this one closes every chat.
  // Leave it as it was found.
  await page.locator(".agent-tab-new").click();
  await expect(tabs()).toHaveCount(1);
  await page.close();
});

// Item 2ni: THE PLAN IS A SETTINGS SECTION, NOT A TAB. "why is plan a chat tab on
// the left side? it was supposed to be right side.. but i decided i think i want it
// under settings, as its own top level item please implement." Everything here is
// read from the running app, because the point of the item is where the entry sits
// and what the strip looks like without it.
test("the Plan is a top-level Settings section, not a tab, and the switch hides the entry", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await expect(page.locator("#chat-log")).toBeVisible();

	// (b): the strip is chats only again. No surface tab, and no one-entry pages nav
	// either — that went with item 2mf and does not come back.
	await expect(page.locator(".agent-tab-wrap-surface")).toHaveCount(0);
	await expect(page.locator(".shell-pages")).toHaveCount(0);
	const chatTab = page.locator(".agent-tab-wrap[data-session]").first();
	await expect(chatTab).toHaveCount(1);
	await expect(page.locator(".agent-tabs .agent-tab-wrap-surface")).toHaveCount(0);

	// (a): one top-level entry, third, after the two the sheet is opened to read,
	// carrying the operator's own prepared mark.
	await page.locator(".shell-settings").click();
	const nav = page.locator(".settings-nav button");
	await expect(nav.nth(2)).toHaveAttribute("data-id", "plan");
	await expect(nav.nth(2)).toHaveText(/Plan/);
	await expect(page.locator('.settings-nav [data-id="plan"] img.shell-page-chip')).toHaveCount(1);

	// The entry exists whatever the planner situation is; what changes is what it
	// shows. On this chat there is no separate planner, so it opens the page.
	await expect(page.locator('.settings-nav [data-id="plan"]')).toBeVisible();

	// (b) and item 2mf (e)/(f): the switch hides the ENTRY now, and brings it back.
	// Nothing about the page is discarded: its address still answers.
	await page.locator('.settings-nav [data-id="chats"]').click();
	await page.locator('[data-action="surface-visible"][data-surface="plan"]').click();
	await expect.poll(async () => page.locator('.settings-nav [data-id="plan"]').count()).toBe(0);
	const direct = await page.request.get(`${harness.base}/plan`);
	expect(direct.status(), "the Plan's own address stopped answering when its entry was hidden").toBe(200);
	await page.locator('[data-action="surface-visible"][data-surface="plan"]').click();
	await expect.poll(async () => page.locator('.settings-nav [data-id="plan"]').count()).toBe(1);
	await page.close();
});

// Item 2nm (c): THE SMALL DESKTOP. "i still can't drag the bottom chat window all the
// way down to collapse the window ... i want to be able to shrink it down."
//
// The frame now states one minimum and nothing else clamps, so the page has to hold at
// that minimum: the client area a 320 x 293 window gives is 304 x 254, and this walks
// the real surface at exactly that. The phone case above proves the enrolled surface at
// 390 px; this one proves the desktop chat at the smallest size the window can be.
test("the chat surface holds at the smallest size the window can be dragged to", async () => {
  const small = await harness.browser.newContext({ viewport: { width: 304, height: 254 } });
  const page = await small.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();
  // A chat has to be OPEN for this to measure anything: the case above closes the last
  // one deliberately, and the empty state has no composer to measure. Measured rather
  // than assumed - the first run of this file after item 2no's cases were added failed
  // on exactly that ordering.
  if (!await page.locator(".agent-tab-wrap[data-session]").count()) {
    await page.locator(".agent-tab-new").click();
  }
  await expect(page.locator("#chat-task")).toBeVisible();
  const seen = await page.evaluate(() => {
    const box = (selector) => {
      const node = document.querySelector(selector);
      return node ? node.getBoundingClientRect() : null;
    };
    const composer = box("#chat-task");
    const send = box("#chat-send");
    const strip = box(".agent-tabs");
    const log = box("#chat-log");
    const clipped = [];
    for (const [name, rect] of Object.entries({ composer, send, strip, log })) {
      if (!rect) { clipped.push(`${name} is not on the page at all`); continue; }
      if (rect.right > window.innerWidth + 1) clipped.push(`${name} runs off the right edge`);
      if (rect.bottom > window.innerHeight + 1) clipped.push(`${name} runs off the bottom edge`);
      if (rect.width < 1 || rect.height < 1) clipped.push(`${name} has no size`);
    }
    // Nothing overlaps the composer: the transcript ends where the composer begins.
    if (log && composer && log.bottom > composer.top + 1) clipped.push("the transcript runs under the composer");
    return {
      clipped,
      sideways: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
      transcript: log ? Math.round(log.height) : 0,
    };
  });
  expect(seen.clipped, seen.clipped.join("; ")).toEqual([]);
  expect(seen.sideways, "the page scrolls sideways at the stated minimum").toBe(false);
  // Three lines of transcript at the 20 px line height the chat log computes to, which
  // is the reason the stated minimum is the number it is.
  expect(seen.transcript, "the transcript has no room for three lines").toBeGreaterThanOrEqual(60);
  await small.close();
});

// Item 2no's acceptance, recorded. "the plan page is wrong — it should be a subpage in
// settings, not a redirect to its own page. the way it is now is bad, it glitches when
// transitioning. i want it like other settings tabs, but refactored to fit nicely."
//
// Four cases, each measuring what is on screen rather than what a handler returned.

// 1. Click Plan in the nav: the pane shows the Plan body, the nav stays selected, the
// address is the Settings pattern, no document is loaded — and the screenshot 50 ms
// after the click is the pane with the body in it.
test("Plan opens in the pane, 50 ms after the click, with no page load", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  let loads = 0;
  page.on("load", () => { loads += 1; });
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();
  const loadsBefore = loads;

  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="plan"]').click();
  await page.waitForTimeout(50);
  const shot = await page.screenshot();
  expect(shot.length, "no screenshot was taken").toBeGreaterThan(0);
  const seen = await page.evaluate(() => {
    const panel = document.querySelector("#settings-page #plan-panel");
    const box = panel?.getBoundingClientRect();
    return {
      inThePane: !!panel,
      drawn: !!box && box.width > 0 && box.height > 0,
      nav: document.querySelector(".settings-nav button.selected")?.dataset?.id || "",
      hash: location.hash,
      path: location.pathname,
      flyout: !!panel?.querySelector(".plan-flyout"),
      document: !!panel?.querySelector(".plan-view"),
    };
  });
  expect(seen.inThePane, "the Plan is not in the Settings pane").toBe(true);
  expect(seen.drawn, "the Plan body has no size 50 ms after the click").toBe(true);
  expect(seen.nav, "the nav entry is not the selected one").toBe("plan");
  // The Settings pattern and nothing else: no /plan in the address.
  expect(seen.hash).toBe("#settings/plan");
  expect(seen.path).not.toBe("/plan");
  expect(seen.flyout && seen.document, "the Plan body is not the whole document").toBe(true);
  expect(loads - loadsBefore, "a document was loaded, which is the transition he watched glitch").toBe(0);
  await page.close();
});

// 2. The narrowest width the window can be dragged to — the minimum v1.33.0 shipped
// (item 2nm): a 320 x 293 window is a 304 x 254 client. No sideways scroll, and the
// flyout and the document stack instead of sitting side by side.
test("the Plan section fits the pane at the narrowest window the frame allows", async () => {
  test.setTimeout(120000);
  const small = await harness.browser.newContext({ viewport: { width: 304, height: 254 } });
  const page = await small.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="plan"]').click();
  await expect(page.locator("#settings-page #plan-panel")).toBeVisible();
  const shot = await page.screenshot();
  expect(shot.length, "no narrowest-width screenshot was taken").toBeGreaterThan(0);
  const seen = await page.evaluate(() => {
    const panel = document.querySelector("#settings-page #plan-panel");
    const box = panel.getBoundingClientRect();
    return {
      columns: getComputedStyle(panel).gridTemplateColumns.split(" ").length,
      offRight: Math.round(box.right) > window.innerWidth + 1,
      sideways: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
      inner: [...panel.querySelectorAll("*")].some((node) => node.scrollWidth > node.clientWidth + 1),
    };
  });
  expect(seen.columns, "the flyout and the document are still side by side").toBe(1);
  expect(seen.offRight, "the Plan runs off the right edge").toBe(false);
  expect(seen.sideways, "the page scrolls sideways").toBe(false);
  expect(seen.inner, "something inside the Plan scrolls sideways").toBe(false);
  await small.close();
});

// 3. /plan, ?from=plan and a deep link with a plan id all land on Settings → Plan.
test("every route the Plan had still lands on the section", async () => {
  test.setTimeout(120000);
  for (const path of ["/plan", "/chat?from=plan", "/plan?plan=none"]) {
    const page = await harness.context.newPage();
    const response = await page.goto(`${harness.base}${path}`);
    expect(response.status(), `${path} did not answer`).toBe(200);
    await expect(page.locator("#settings-page #plan-panel"), `${path} did not land on the Plan section`).toBeVisible();
    await expect(page.locator(".settings-nav button.selected")).toHaveAttribute("data-id", "plan");
    await page.close();
  }
});

// 4. The availability rule (item 2ni (b), kept by 2no (e)): with a planner assigned to
// another connection and this chat not the planner's, the entry is still there and the
// pane carries the one line saying what to do about it.
test("an unassigned planner leaves the entry and shows the one line in the pane", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  // Give the agent a planner that is not this chat's role, which is what makes the
  // Plan unavailable on this chat.
  await page.locator('.settings-nav [data-id="agents"]').click();
  await expect(page.locator("#panel-roles")).toBeVisible();
  const planner = page.locator('#panel-roles select[data-role="d"]');
  const choice = await planner.locator("option").evaluateAll((options) =>
    options.map((option) => option.value).find((value) => value));
  if (choice) {
    await planner.selectOption(choice);
    await page.waitForTimeout(800);
  }
  await page.locator('.settings-nav [data-id="plan"]').click();
  await expect(page.locator('.settings-nav [data-id="plan"]'), "the entry went away").toBeVisible();
  const note = page.locator(".settings-plan-note");
  const panel = page.locator("#settings-page #plan-panel");
  // Either the document is there because this chat can plan, or the one line is —
  // never neither, and never a blank pane.
  expect(await note.count() + await panel.count(), "the Plan section is empty").toBeGreaterThan(0);
  if (await note.count()) {
    await expect(note).toContainText("Assign a planner in Agents");
    await expect(panel).toHaveCount(0);
  }
  await page.close();
});
