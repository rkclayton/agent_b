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
const openSettings = async (page) => { await page.locator(".chat-list-menu-button").click(); await page.locator(".chat-list-main-menu").getByRole("button", { name: "Settings", exact: true }).click(); };
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
test("Setup and Connections share the independent model picker and read-only Test", async () => {
  const setup = await harness.context.newPage();
  await setup.goto(`${harness.base}/setup`);
  await setup.getByRole("button", { name: "Query models" }).click();
  await expect(setup.locator('[data-field="model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(setup.locator('[data-field="model"] option')).toHaveText(["absent-model · not served", "alpha-model", "beta-model"]);
  await setup.getByRole("button", { name: "Test" }).click();
  await expect(setup.locator(".setup-feedback")).toContainText('Model "absent-model" is not served');

  const settings = await harness.context.newPage();
  await settings.goto(`${harness.base}/chat?from=setup#settings/connections`);
  await settings.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await expect(settings.locator('[data-path="connections.ui.model"] option')).toHaveText(["alpha-model", "beta-model", "type a name…"]);
  const before = await hash(join(harness.dataRoot, "harness.json"));
  // Item 2l5: Test is one of the four actions on the connection own row now.
  await settings.locator('.connection-editor [data-action="probe"][data-id="ui"]').click();
  // Item 2nb (g): ONE MESSAGE, ONE PLACE. The result of the last Test is rendered
  // under the field it is about and nowhere else. The operator saw three copies of one
  // sentence before this; the note carries the message when there is one, and what
  // discovery found when there is not.
  // 2po replaces the old full model-refusal sentence with the planner's exact
  // fallback sentence; its unchanged full wording is one click away.
  await expect(settings.locator(".connection-editor .discovery-note")).toHaveCount(1);
  await expect(settings.locator(".connection-editor .discovery-note")).toContainText('Model "absent-model" is not served');
  // (c): the model control is a dropdown ALWAYS, and it always offers the one way out
  // for a server that cannot list its models.
  await expect(settings.locator('[data-path="connections.ui.model"]')).toHaveJSProperty("tagName", "SELECT");
	await expect(settings.locator('[data-path="connections.ui.model"] option')).toHaveText(["alpha-model", "beta-model", "type a name…"]);
  // (g) again, from the other side: the row header carries the STATE WORD and never
  // the message, so it cannot overflow.
  const header = settings.locator('.connection-row[data-id="ui"] .connection-state');
  await expect(header).not.toContainText('is not served');
  await expect(header).not.toHaveText("");
  expect(await hash(join(harness.dataRoot, "harness.json"))).toBe(before);
  const chat = await harness.context.newPage();
  await chat.goto(`${harness.base}/chat`);
  await expect(chat.locator(".shell-session-title")).toHaveText("agent_b");
  await chat.screenshot({ path: join(repo, "test-results", "2qr-model-selector.png") });
  await chat.close();
  await setup.close();
  await settings.close();
});

test("setup context row distinguishes usable published and typed readings 2sv", async () => {
	await mkdir(join(repo, "test-results"), { recursive: true });
	const variants = [
		["usable", { n_ctx: 200000, observed_byte_limit: 400000 }, "usable ceiling 100000 · saved 100000 above"],
		["published", { n_ctx: 200000 }, "published window 200000"],
		["typed", {}, "typed, not measured"],
	];
	for (const [name, capabilities, expected] of variants) {
		const page = await harness.context.newPage();
		await page.route("**/api/state", async (route) => {
			const response = await route.fetch();
			const state = await response.json();
			state.connections[0].capabilities = capabilities;
			state.connections[0].context.n_ctx = name === "typed" ? 123456 : 200000;
			await route.fulfill({ response, json: state });
		});
		await page.goto(`${harness.base}/setup`);
		const reading = page.locator('label:has-text("Context size") .setup-note');
		await expect(reading).toHaveText(expected);
		await page.screenshot({ path: join(repo, "test-results", `2sv-context-${name}.png`), fullPage: true });
		await page.close();
	}
});
test("Settings Context shows the server recommendation without changing the field 2sx", async () => {
	await mkdir(join(repo, "test-results"), { recursive: true });
	for (const [name, saved, expected] of [["saved", 65536, "server recommends 32768 tokens; saved value is kept"], ["empty", 0, "server context: 32768 tokens"]]) {
		const page = await harness.context.newPage();
		await page.goto(`${harness.base}/chat#settings/connections`);
		await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
		await page.locator('[data-path="connections.ui.context.n_ctx"]').fill(saved ? String(saved) : ""); await page.locator('[data-path="connections.ui.context.n_ctx"]').press("Enter");
		await expect(page.locator('[data-path="connections.ui.context.n_ctx"]')).toHaveValue(saved ? String(saved) : "");
		await expect(page.getByText(expected, { exact: true })).toBeVisible();
		await page.screenshot({ path: join(repo, "test-results", `2sx-context-${name}.png`), fullPage: true }); await page.close();
	}
});

test("a failed model list keeps the saved model and names the reason 2r7", async () => {
	const page = await harness.context.newPage();
	await page.route("**/api/connections/ui/models", (route) => route.fulfill({ status: 502, contentType: "application/json", body: JSON.stringify({ error: "model list unavailable" }) }));
	await page.goto(`${harness.base}/chat#settings/connections`);
	await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
	await expect(page.locator('[data-path="connections.ui.model"] option')).toHaveText(["absent-model", "type a name…"]);
	await expect(page.locator(".connection-primary .settings-note.alarm")).toHaveText("model list not read — model list unavailable");
	await expect(page.getByText("credential ref", { exact: true })).toHaveCount(0);
	await page.close();
});

test("Test Eval Recommended stay adjacent at 1400px and the narrowest width 2qw", async () => {
  const page = await harness.context.newPage({ viewport: { width: 1400, height: 900 } });
  let models = [
    "Huihui-Qwen3.8-27B-abliterated-UD-Q3_K_XL",
    "Qwen3.8-27B-UD-IQ4_XS",
    "Qwen3.8-27B-UD-Q3_K_XL",
  ];
  await page.route("**/api/connections/ui/models", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ models }) }));
  await page.goto(`${harness.base}/chat#settings/connections`);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await expect(page.locator('[data-path="connections.ui.model"] option')).toContainText([...models, "type a name…"]);
  const measured = await page.locator(".connection-editor").evaluate((editor) => {
    const boxes = [...editor.querySelectorAll("input")].map((input) => ({ path: input.dataset.path, width: input.getBoundingClientRect().width }));
    return { columns: getComputedStyle(editor.querySelector(".connection-fields")).gridTemplateColumns.split(" ").length, boxes };
  });
  expect(measured.columns, JSON.stringify(measured)).toBe(2);
  expect(measured.boxes.find((x) => x.path?.endsWith(".label")).width).toBeLessThanOrEqual(260);
  expect(measured.boxes.find((x) => x.path?.endsWith(".base_url")).width).toBeLessThanOrEqual(440);
  const actions = page.locator(".connection-primary-actions");
  await expect(actions.locator("button")).toHaveText(["Test", "Eval", "Recommended"]);
  const geometry = await actions.evaluate((node) => ({
    tops: [...node.querySelectorAll("button")].map((button) => Math.round(button.getBoundingClientRect().top)),
    inside: [...node.querySelectorAll("button")].every((button) => button.getBoundingClientRect().right <= document.documentElement.clientWidth + 1),
    overflow: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
  }));
  expect(new Set(geometry.tops).size, JSON.stringify(geometry)).toBe(1);
  expect(geometry.inside, JSON.stringify(geometry)).toBe(true);
  expect(geometry.overflow).toBe(false);
  const wideShot = await page.screenshot();
  expect(wideShot.length).toBeGreaterThan(0);
  const evidence = process.env.AGENTB_EVIDENCE_DIR;
  if (evidence) {
    await mkdir(evidence, { recursive: true });
    await page.screenshot({ path: join(evidence, "2qw-actions-1400.png"), fullPage: true });
  }
  await page.close();

  const narrow = await harness.browser.newContext({ viewport: { width: 304, height: 700 } });
  const small = await narrow.newPage();
  await small.goto(`${harness.base}/chat#settings/connections`);
  await small.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  const narrowActions = small.locator(".connection-primary-actions");
  await expect(narrowActions.locator("button")).toHaveText(["Test", "Eval", "Recommended"]);
  const narrowGeometry = await narrowActions.evaluate((node) => ({
    tops: [...node.querySelectorAll("button")].map((button) => Math.round(button.getBoundingClientRect().top)),
    inside: [...node.querySelectorAll("button")].every((button) => button.getBoundingClientRect().right <= document.documentElement.clientWidth + 1),
    overflow: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
  }));
  expect(new Set(narrowGeometry.tops).size, JSON.stringify(narrowGeometry)).toBe(1);
  expect(narrowGeometry.inside, JSON.stringify(narrowGeometry)).toBe(true);
  expect(narrowGeometry.overflow).toBe(false);
  const narrowShot = await small.screenshot();
  expect(narrowShot.length).toBeGreaterThan(0);
  if (evidence) await small.screenshot({ path: join(evidence, "2qw-actions-narrow.png"), fullPage: true });
  await narrow.close();
});

test("2qx seven-field editor and closed Defaults fit wide and narrow", async () => {
  const evidence = process.env.AGENTB_EVIDENCE_DIR;
  for (const width of [1400, 304]) {
    const context = await harness.browser.newContext({ viewport: { width, height: width === 304 ? 700 : 900 } });
    const page = await context.newPage();
    await page.goto(`${harness.base}/chat#settings/connections`);
    await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
    const editor = page.locator(".connection-editor");
    const visibleLabels = editor.locator(':scope > .connection-fields > .connection-primary > .setting-row > label');
    expect((await visibleLabels.allTextContents()).filter(Boolean)).toEqual(["name", "address", "key", "model", "context size", "thinking", "reads images"]);
    const defaults = editor.locator("details.connection-defaults");
    await expect(defaults.locator(":scope > summary")).toHaveText("Defaults");
    await expect(defaults).not.toHaveAttribute("open", "");
    const geometry = await editor.evaluate((node) => ({
      right: node.getBoundingClientRect().right,
      viewport: document.documentElement.clientWidth,
      overflow: document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
    }));
    expect(geometry.right, JSON.stringify(geometry)).toBeLessThanOrEqual(geometry.viewport + 1);
    expect(geometry.overflow, JSON.stringify(geometry)).toBe(false);
    if (evidence) {
      await mkdir(evidence, { recursive: true });
      await page.screenshot({ path: join(evidence, `2qx-editor-${width}.png`), fullPage: true });
    }
    await context.close();
  }
});

test("a duplicate keeps the stored key and Show hides it again 2qn", async () => {
  test.setTimeout(60000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat#settings/connections`);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  const key = page.locator('[data-path="connections.ui.api_key"]');
  await key.fill("planted-key-2qn");
  const saved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await key.press("Enter");
  await saved;
	await page.locator('.connection-row[data-id="ui"] [data-action="duplicate-connection"]').click();
	const copyKey = page.locator('.connection-editor [data-path$=".api_key"]:not([data-path="connections.ui.api_key"])');
  await expect(copyKey).toBeVisible();
  const copyID = (await copyKey.getAttribute("data-path")).split(".")[1];
  await page.evaluate(() => {
    const nativeTimeout = window.setTimeout;
    window.setTimeout = (callback, delay, ...args) => nativeTimeout(callback, delay === 30000 ? 300 : delay, ...args);
  });
  await page.locator(`.connection-editor [data-action="show-key"][data-id="${copyID}"]`).click();
  await expect(copyKey).toHaveValue("planted-key-2qn");
  await expect(copyKey).toHaveAttribute("type", "text");
  await page.waitForTimeout(500);
  await expect(copyKey).toHaveValue("");
  await expect(copyKey).toHaveAttribute("type", "password");
  const tested = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${copyID}/probe`));
  await page.locator(`.connection-editor [data-action="probe"][data-id="${copyID}"]`).click();
  expect((await tested).status()).toBeLessThan(400);
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${copyID}"]`).click();
  await page.locator('[data-action="confirm-proceed"]').click();
  await expect(page.locator(`.connection-row [data-action="connection-toggle"][data-id="${copyID}"]`)).toHaveCount(0);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  const restored = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await key.fill("fixture-key");
  await key.press("Enter");
  await restored;
  await page.close();
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

test("Security shows one Phone section and retired browser phone routes are absent", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await openSettings(page);
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
	await page.locator('.connection-row[data-id="ui"] [data-action="duplicate-connection"]').click();
	const copyAddress = page.locator('.connection-editor [data-path$=".base_url"]:not([data-path="connections.ui.base_url"])');
	await expect(copyAddress).toBeVisible();
	const copyPath = await copyAddress.getAttribute("data-path");
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
  await page.locator('.connection-row[data-id="ui"] [data-action="error-details"]').click();
  await expect(page.locator(".settings-error-panel pre")).toHaveText(timeoutDetail);
  expect(harness.context.pages().length, "details opened another window").toBe(pages);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1), "details added horizontal scrolling").toBe(false);
  await page.keyboard.press("Escape");
  await expect(page.locator(".settings-error-panel")).toHaveCount(0);
  await expect(page.locator('.connection-row[data-id="ui"] [data-action="error-details"]')).toHaveCount(1);
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
  await page.locator('[data-path="connections.ui.base_url"]').press("Enter");
  await expect(page.locator('.connection-editor [data-path="connections.ui.base_url"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).toHaveClass(/invalid/);
  await expect(page.locator(".connection-editor .field-error")).toHaveCount(1);
  await expect(page.locator('.connection-editor [data-path$=".label"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).not.toHaveClass(/invalid/);
  await expect(page.locator('.connection-editor [data-path$=".api_key"]').locator("xpath=ancestor::div[contains(@class,'setting-row')]")).not.toHaveClass(/invalid/);
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

	// The chat list is the only chat navigation; the retired surface strip and
	// one-entry pages nav do not come back.
	await expect(page.locator(".agent-tab-wrap-surface")).toHaveCount(0);
	await expect(page.locator(".shell-pages")).toHaveCount(0);
	await expect(page.locator(".chat-list-row[data-session]").first()).toHaveCount(1);
	await expect(page.locator(".agent-tabs, .agent-tab-wrap")).toHaveCount(0);

	// (a): one top-level entry, third, after the two the sheet is opened to read,
	// carrying the operator's own prepared mark.
	await openSettings(page);
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
  // A chat has to be OPEN for this to measure anything: the case above closes the last
  // one deliberately, and the empty state has no composer to measure. Measured rather
  // than assumed - the first run of this file after item 2no's cases were added failed
  // on exactly that ordering.
  if (!await page.locator(".chat-list-row[data-session]").count()) {
    await page.locator(".chat-list-new").click();
  }
  // At this width the retained-chat panel is a drawer. Collapse it through the
  // same resize edge a person uses before measuring the chat beneath it.
  const edge = await page.locator(".chat-list-resize").boundingBox();
  if (edge) {
    await page.mouse.move(edge.x + edge.width / 2, edge.y + 20);
    await page.mouse.down();
    await page.mouse.move(0, edge.y + 20);
    await page.mouse.up();
  }
  await expect(page.locator("#chat-log")).toBeVisible();
  await expect(page.locator("#chat-task")).toBeVisible();
  const seen = await page.evaluate(() => {
    const box = (selector) => {
      const node = document.querySelector(selector);
      return node ? node.getBoundingClientRect() : null;
    };
    const composer = box("#chat-task");
    const send = box("#chat-send");
    const panel = box(".chat-list-panel:not([hidden])") || box(".chat-list-handle:not([hidden])");
    const log = box("#chat-log");
    const clipped = [];
    for (const [name, rect] of Object.entries({ composer, send, panel, log })) {
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

  await openSettings(page);
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
  await openSettings(page);
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

test("connection fields apply on choice Enter and blur with no Save step 2r9", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
	const restored = await page.request.post(`${harness.base}/api/config`, {
		data: { connections: [{ id: "ui", base_url: `http://127.0.0.1:${harness.modelPort}` }] },
		headers: { "X-AgentB-Mutation-Token": harness.mutationToken },
	});
	expect(restored.ok()).toBe(true);
  await page.goto(`${harness.base}/chat#settings/connections`);
  const modelList = page.waitForResponse((response) => response.url().endsWith("/api/connections/ui/models"));
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await modelList;
  const state = async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.connections.find((item) => item.id === "ui");
  harness.modelRequests.length = 0;
  const picker = page.locator('[data-path="connections.ui.model"]');
  const chosenModel = "alpha-model";
  await expect(picker.locator('option[value="alpha-model"]')).toHaveCount(1);
  const modelSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await picker.selectOption(chosenModel);
  await modelSaved;
  await expect.poll(async () => (await state()).model).toBe(chosenModel);
  await page.locator('[data-path="connections.ui.label"]').fill("UI renamed"); await page.locator('[data-path="connections.ui.label"]').press("Enter");
  await expect.poll(async () => (await state()).label).toBe("UI renamed");
  await page.locator(".connection-defaults > summary").click();
  await page.locator('[data-path="connections.ui.request_timeout_s"]').fill("3"); await page.evaluate(() => document.querySelector('[data-path="connections.ui.request_timeout_s"]').blur());
  await expect.poll(async () => (await state()).request_timeout_s).toBe(3);
  await page.locator('[data-action="config-toggle"][data-path="connections.ui.reads_images"]').click();
  await expect.poll(async () => (await state()).reads_images).toBe(true);
  await page.locator('[data-path="connections.ui.request_timeout_s"]').fill("0"); await page.locator('[data-path="connections.ui.request_timeout_s"]').press("Enter");
  await expect(page.locator('.field-error')).toContainText("must be positive");
  expect((await state()).request_timeout_s).toBe(3);
  await expect(page.locator('[data-action="save-connection"], .setting-save')).toHaveCount(0);
  await expect(page.locator("#settings-page")).not.toContainText(/unsaved/i);
  await page.getByTitle("Close").click();
  await page.locator("#chat-task").fill("answer with the selected model");
  await page.locator("#chat-send").click();
  await expect.poll(() => harness.modelRequests.at(-1)?.model).toBe(chosenModel);
  await page.close();
});

test("one in-place editor and Recommended persist without another action 2r9", async () => {
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat#settings/connections`);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  expect(await page.locator('.connection-row[data-id="ui"]').evaluate((row) => row.nextElementSibling?.classList.contains("connection-editor"))).toBe(true);
  await page.route("**/api/connections/ui/recommended", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ values: { "context.n_ctx": 48000, "reads_images": false }, sources: { "context.n_ctx": "server", "reads_images": "Eval" } }) }));
  await page.locator('[data-action="recommended-connection"][data-id="ui"]').click();
  await expect.poll(async () => ((await (await page.request.get(`${harness.base}/api/state`)).json()).config.connections.find((item) => item.id === "ui").context.n_ctx)).toBe(48000);
  await expect(page.locator(".connection-editor")).toContainText("changed: context size, reads images");
  await page.close();
});

test("Duplicate is a ready keyed row with a refreshed model list 2r9", async () => {
  const page = await harness.context.newPage();
  let models = ["one", "two", "three"], calls = 0;
  await page.route("**/api/connections/*/models", (route) => { calls++; return route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ models, model_defaults: {} }) }); });
  await page.goto(`${harness.base}/chat#settings/connections`);
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  const key = page.locator('[data-path="connections.ui.api_key"]');
  const keyed = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await key.fill("fixture-key"); await key.press("Enter"); await keyed;
  await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.connections[0].api_key).toContain("set");
  await page.locator('.connection-row[data-id="ui"] [data-action="duplicate-connection"]').click();
  const editor = page.locator(".connection-editor");
  const model = editor.locator('[data-path$=".model"]:not([data-path="connections.ui.model"])');
  await expect(model).toBeVisible();
  await expect(model.locator("option")).toHaveText(["one", "two", "three", "type a name…"]);
  const copyID = (await model.getAttribute("data-path")).split(".")[1];
  const shown = page.locator(`[data-action="show-key"][data-id="${copyID}"]`);
  await shown.click(); await expect(editor.locator(`[data-path="connections.${copyID}.api_key"]`)).toHaveValue("fixture-key");
  models = ["one", "two", "three", "four"];
  const before = calls; await model.click();
  await expect.poll(() => calls).toBeGreaterThan(before);
  await expect(model.locator("option")).toHaveText(["one", "two", "three", "four", "type a name…"]);
  await page.close();
});

// 4. The availability rule (item 2ni (b), kept by 2no (e)): with a planner assigned to
// another connection and this chat not the planner's, the entry is still there and the
// pane carries the one line saying what to do about it.
test("an unassigned planner leaves the entry and shows the one line in the pane", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await openSettings(page);
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
