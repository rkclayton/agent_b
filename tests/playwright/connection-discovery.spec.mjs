import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdtemp, readFile } from "node:fs/promises";
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
    connectionModel: "model",
  });
});

test.afterAll(async () => {
  await harness?.stop();
  if (root) removeTreeWithinAllowedRoots(root, [tmpdir()], "connection-discovery Playwright cleanup");
});

test("Setup and Connections share endpoint discovery and the model picker", async () => {
  const setup = await harness.context.newPage();
  await setup.goto(`${harness.base}/setup`);
  await setup.getByRole("button", { name: "Test" }).click();
  await expect(setup.locator(".discovery-note")).toHaveText(`found http://127.0.0.1:${harness.modelPort}`);
  await expect(setup.locator('[data-field="model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(setup.locator('[data-field="model"] option')).toHaveText(["model · not served", "alpha-model", "beta-model"]);
  await expect(setup.locator(".setup-feedback")).toContainText('Model "model" is not served');

  const settings = await harness.context.newPage();
  await settings.goto(`${harness.base}/chat?from=setup#settings/connections`);
  await settings.locator('.connection-summary[data-id="ui"]').click();
  const before = await hash(join(harness.dataRoot, "harness.json"));
  // Item 2l5: Test is one of the four actions on the connection own row now.
  await settings.locator('.connection-row [data-action="probe"][data-id="ui"]').click();
  // Item 2nb (g): ONE MESSAGE, ONE PLACE. The result of the last Test is rendered
  // under the field it is about and nowhere else. The operator saw three copies of one
  // sentence before this; the note carries the message when there is one, and what
  // discovery found when there is not.
  await expect(settings.locator(".connection-editor .discovery-note")).toHaveCount(1);
  await expect(settings.locator(".connection-editor .discovery-note")).toContainText('Model "model" is not served');
  // (c): the model control is a dropdown ALWAYS, and it always offers the one way out
  // for a server that cannot list its models.
  await expect(settings.locator('[data-path="connections.ui.model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(settings.locator('[data-path="connections.ui.model"] option')).toHaveText(["model", "alpha-model", "beta-model", "type a name…"]);
  // (g) again, from the other side: the row header carries the STATE WORD and never
  // the message, so it cannot overflow.
  const header = settings.locator('.connection-row:has(.connection-summary[data-id="ui"]) .connection-state');
  await expect(header).not.toContainText('is not served');
  await expect(header).not.toHaveText("");
  expect(await hash(join(harness.dataRoot, "harness.json"))).toBe(before);
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
		if (!await page.locator(".connection-editor").count()) await page.locator('.connection-summary[data-id="ui"]').click();
		await expect(page.locator(".connection-editor")).toBeVisible();
		await page.locator('.connection-editor [data-path$=".label"]').fill(`UI ${save ? "saved" : "discarded"}`);
		page.once("dialog", (dialog) => save ? dialog.accept() : dialog.dismiss());
		await page.locator(`.agent-tab[data-session="${session}"]`).click();
		await expect(page.locator("#settings-page")).toBeHidden();
		await expect(page.locator("#chat-log")).toBeVisible();
	}
});

test("the phone viewport enrols into the shared chat surface without horizontal overflow", async () => {
	const desktop = await harness.context.newPage();
	await desktop.goto(`${harness.base}/chat`);
	const token = await desktop.locator('meta[name="agentb-mutation-token"]').getAttribute("content");
	const offer = await desktop.evaluate(async (mutationToken) => {
		const response = await fetch("/api/phone/enrolment", { method: "POST", headers: { "X-AgentB-Mutation-Token": mutationToken } });
		return response.json();
	}, token);
	const phoneContext = await harness.browser.newContext({ viewport: { width: 390, height: 844 } });
	const phone = await phoneContext.newPage();
	await phone.goto(`${harness.base}/phone`);
	await expect(phone.locator("#phone-enrol")).toBeVisible();
	await phone.locator("#phone-name").fill("Playwright phone");
	await phone.locator("#phone-code").fill(offer.code);
	await phone.locator("#phone-enrol-form button").click();
	await expect(phone.locator("#phone-chat")).toBeVisible();
	await expect(phone.locator("#phone-composer")).toBeVisible();
	expect(await phone.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth)).toBe(true);
	await phoneContext.close();
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
});
