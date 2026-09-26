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
  await expect(settings.locator(".connection-editor .discovery-note")).toHaveText(`found http://127.0.0.1:${harness.modelPort}`);
  await expect(settings.locator('[data-path="connections.ui.model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(settings.locator('[data-path="connections.ui.model"] option')).toHaveText(["model", "alpha-model", "beta-model"]);
  await expect(settings.locator('.connection-row:has(.connection-summary[data-id="ui"]) .connection-state')).toContainText('Model "model" is not served');
  expect(await hash(join(harness.dataRoot, "harness.json"))).toBe(before);
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
