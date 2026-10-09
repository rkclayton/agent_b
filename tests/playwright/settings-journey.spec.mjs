// Item 2mo (a) and (b'): THE SETTINGS JOURNEY, FIRST, BECAUSE IT IS THE ONE THAT
// FAILED HIM.
//
// An external review put it plainly: "Component suites passed while the user
// workflow failed." Every package suite this product has was green on 2026-09-27
// while the operator could not add or remove a connection. The gates covered slices;
// none of them walked a thing an operator actually does from end to end.
//
// So this walks it, and every step asserts WHAT IS ON SCREEN — not an API response,
// not a configuration file. Nine journeys are named by the item; this is the first,
// and the overlaps rel-1.31.0/W0 measured are named in the release report rather than
// rewritten here: simple task, tool task, follow-up, restart and interruption are
// already carried by the chat acceptance's 72 recorded proofs.
//
// (b): deterministic, against the harness's controlled model server, so a failure is
// a product defect and not a model mood.
import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { start } from "../ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "../../tools/removal-guard.mjs";

const run = promisify(execFile);
const repo = resolve(fileURLToPath(new URL("../..", import.meta.url)));
const toggleSettings = async (page) => {
	if (await page.locator("#settings-page").isVisible()) return page.getByTitle("Close").click();
	await page.locator(".chat-list-menu-button").click();
	await page.locator(".chat-list-main-menu").getByRole("button", { name: "Settings", exact: true }).click();
};
let root;
let harness;
let hermesHome;

test.beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "agentb-settings-journey-"));
	hermesHome = join(root, "hermes");
	await mkdir(join(hermesHome, "memories"), { recursive: true });
	await mkdir(join(hermesHome, "skills", "writing", "portable"), { recursive: true });
	await mkdir(join(hermesHome, "cron"), { recursive: true });
	await mkdir(join(hermesHome, "sessions"), { recursive: true });
	await writeFile(join(hermesHome, "SOUL.md"), "fixture persona");
	await writeFile(join(hermesHome, "memories", "MEMORY.md"), "fixture memory");
	await writeFile(join(hermesHome, "memories", "USER.md"), "fixture user");
	await writeFile(join(hermesHome, "skills", "writing", "portable", "SKILL.md"), "---\nname: portable\ndescription: Portable preview fixture.\n---\nRead the request.\n");
	await writeFile(join(hermesHome, ".env"), "FIRST_KEY=\nSECOND_KEY=\n");
  const exe = join(root, "Agent_b.exe");
  await run(join(repo, ".tools", "go", "bin", "go.exe"), ["build", "-o", exe, "./cmd/harness"], { cwd: repo, windowsHide: true });
  harness = await start({ exe, appRoot: repo, data: join(root, "data"), modelIDs: ["journey-model", "second-model"], streamDelayMs: 800 });
});

test("Hermes import shows the complete preview before writing", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="profiles"]').click();
	await page.locator("#hermes-import-path").fill(hermesHome);
	await page.locator('[data-action="hermes-preview"]').click();
	await expect(page.locator(".hermes-row")).toHaveCount(8);
	for (const name of ["SOUL.md", "MEMORY.md", "USER.md", "portable", "FIRST_KEY", "SECOND_KEY", "cron", "sessions"]) await expect(page.locator("#settings-page")).toContainText(name);
	await expect(page.locator("#settings-page")).toContainText("preview only · nothing written");
	const evidence = join(repo, "logs", "evidence", "rel-1.58.0"); await mkdir(evidence, { recursive: true });
	await page.screenshot({ path: join(evidence, "hermes-preview.png"), fullPage: true });
	await page.close();
});

test.afterAll(async () => {
  await harness?.stop();
  if (root) removeTreeWithinAllowedRoots(root, [tmpdir()], "settings-journey Playwright cleanup");
});

test("Settings is a centred bounded window over the drawn chat 2sy", async () => {
	const page = await harness.context.newPage();
	for (const width of [1280, 1920]) {
		await page.setViewportSize({ width, height: 900 }); await page.goto(`${harness.base}/chat`); await toggleSettings(page);
		const box = await page.evaluate(() => { const panel = document.querySelector("#settings-page").getBoundingClientRect(), strip = document.querySelector("#app-shell").getBoundingClientRect(); return { width: panel.width, height: panel.height, dx: Math.abs(panel.left + panel.width / 2 - innerWidth / 2), dy: Math.abs(panel.top + panel.height / 2 - (strip.bottom + (innerHeight - strip.bottom) / 2)), chat: document.querySelector("#chat-log").checkVisibility(), list: document.querySelector(".chat-list-panel").checkVisibility(), backdrop: document.querySelector("#settings-backdrop")?.checkVisibility() || false }; });
		expect(box).toMatchObject({ width: 960, height: 720, chat: true, list: true, backdrop: true }); expect(box.dx).toBeLessThanOrEqual(1); expect(box.dy).toBeLessThanOrEqual(1);
		await page.screenshot({ path: join(repo, "test-results", `2sy-settings-${width}.png`), fullPage: true }); await page.getByTitle("Close").click();
		await expect(page.locator("#settings-page")).toBeHidden();
	}
	await page.close();
});

test("Settings fills narrow windows and its X closes to prior focus 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`);
	for (const width of [304, 600]) for (const size of [9, 12, 32]) {
		await page.setViewportSize({ width, height: 700 }); await page.locator(".chat-list-menu-button").focus(); await page.evaluate((value) => document.documentElement.style.setProperty("--chat-scale", String(value / 12)), size); await page.evaluate(() => document.dispatchEvent(new CustomEvent("settings.open")));
		const geometry = await page.locator("#settings-page").evaluate((panel) => { const box = panel.getBoundingClientRect(), strip = document.querySelector("#app-shell").getBoundingClientRect(), close = panel.querySelector('[data-action="close"]').getBoundingClientRect(), head = panel.querySelector(".settings-head").getBoundingClientRect(), content = panel.querySelector(".settings-content").getBoundingClientRect(), group = panel.querySelector(".settings-group").getBoundingClientRect(); return { left: box.left, right: innerWidth - box.right, top: box.top - strip.bottom, bottom: innerHeight - box.bottom, contentLeft: content.left - box.left, contentRight: box.right - content.right, groupLeft: group.left - box.left, groupRight: box.right - group.right, overflow: document.documentElement.scrollWidth - innerWidth, closeWhole: close.left >= head.left && close.right <= head.right && close.top >= head.top && close.bottom <= head.bottom }; });
		expect({ ...geometry, groupRight: undefined }).toEqual({ left: 0, right: 0, top: 0, bottom: 0, contentLeft: 0, contentRight: 0, groupLeft: 12, groupRight: undefined, overflow: 0, closeWhole: true }); expect(geometry.groupRight).toBeGreaterThanOrEqual(12); await page.screenshot({ path: join(repo, "test-results", `2sy-settings-${width}-${size}px.png`), fullPage: true }); await page.getByTitle("Close").press("Enter"); await expect(page.locator(".chat-list-menu-button")).toBeFocused();
	}
	await page.close();
});

test("only the dimmed chat closes Settings; the strip does not 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await toggleSettings(page); await expect(page.locator(".shell-settings")).toHaveCount(0);
	const strip = await page.locator("#app-shell").boundingBox(); await page.mouse.click(strip.x + 200, strip.y + 12); await page.mouse.dblclick(strip.x + 220, strip.y + 12); await page.mouse.move(strip.x + 250, strip.y + 12); await page.mouse.down(); await page.mouse.move(strip.x + 300, strip.y + 12); await page.mouse.up(); await expect(page.locator("#settings-page")).toBeVisible();
	await page.locator("#settings-backdrop").click({ position: { x: 4, y: 4 } }); await expect(page.locator("#settings-page")).toBeHidden(); await page.close();
});

test("Settings traps focus and blocks keys from the chat behind it 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await page.locator("#chat-task").fill("held"); await toggleSettings(page);
	const controls = page.locator('#settings-page button:not([disabled]),#settings-page input:not([disabled]),#settings-page textarea:not([disabled]),#settings-page select:not([disabled]),#settings-page [tabindex="0"]'); const count = await controls.count(); await controls.nth(count - 1).focus(); await page.keyboard.press("Tab"); await expect(controls.first()).toBeFocused(); await page.keyboard.type("x"); await expect(page.locator("#chat-task")).toHaveValue("held"); await page.close();
});

test("a live run keeps drawing beneath Settings 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); const original = await page.locator(".chat-list-row.selected").getAttribute("data-session"); await page.locator(".chat-list-new").click(); await expect(page.locator(".chat-list-row.selected")).not.toHaveAttribute("data-session", original); const streamed = await page.locator(".chat-list-row.selected").getAttribute("data-session"); await page.locator("#chat-task").fill("stream behind Settings"); await page.locator("#chat-send").click();
	await expect(page.locator("#chat-log")).toContainText("ui harness "); await toggleSettings(page); await expect(page.locator(".chat-list-name.working")).toBeVisible();
	const before = (await page.locator("#chat-log").innerText()).length; await page.screenshot({ path: join(repo, "test-results", "2sy-settings-streaming.png"), fullPage: true });
	await expect(page.locator("#chat-log")).toContainText("ui harness reply"); await expect.poll(async () => (await page.locator("#chat-log").innerText()).length).toBeGreaterThan(before); await expect(page.locator(".chat-list-name.working")).toHaveCount(0); await page.getByTitle("Close").click(); await page.locator(`.chat-list-row[data-session="${original}"] .chat-list-name`).click(); const streamRow = page.locator(`.chat-list-row[data-session="${streamed}"]`); await streamRow.hover(); await streamRow.locator(".chat-list-more").click(); page.once("dialog", (dialog) => dialog.accept()); await streamRow.locator(".chat-list-row-menu").getByRole("button", { name: "Delete", exact: true }).click(); await expect(streamRow).toHaveCount(0); await page.close();
});

test("the setup document uses an X and returns to Settings 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await toggleSettings(page); await page.locator('[data-action="open-setup"]').click();
	await expect(page).toHaveURL(/\/setup\?from=settings/); await expect(page.locator(".setup-close")).toHaveText("×"); await expect(page.locator(".setup-close")).toHaveAttribute("title", "Close"); await expect(page.locator("body")).not.toContainText("⚙");
	await page.locator(".setup-close").click(); await expect(page).toHaveURL(/\/chat\?.*#settings\/connections/); await expect(page.locator("#settings-page")).toBeVisible(); await page.close();
});

test("the settings journey: type a host, pick a model, Test, save, chat, delete, remove", async () => {
  // Ten steps including a model round trip: the default 30 seconds is the wrong
  // bound for a journey, and a journey cut short reads as a product failure.
  test.setTimeout(180000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();

  // 1. Open Settings and its Connections section, on screen.
  await toggleSettings(page);
  await expect(page.locator("#settings-page")).toBeVisible();
  await page.locator('.settings-nav [data-id="connections"]').click();
  const rowsBefore = await page.locator(".connection-row").count();

  // 2. Add a connection. The row appears and its editor is open.
  await page.locator('[data-action="add-connection"]').click();
  await expect(page.locator(".connection-row")).toHaveCount(rowsBefore + 1);
  const editor = page.locator(".connection-editor");
  await expect(editor).toBeVisible();
  const path = await editor.locator("[data-path$='.base_url']").getAttribute("data-path");
  const id = path.split(".")[1];

  // 3. TYPE A HOST. He types where his server is; the port he typed is the one
  // discovery tries first (item 2nb (a)).
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill(`http://127.0.0.1:${harness.modelPort}`);

	// 4. Name the model this connection should test. Model-list discovery has its own
	// focused journey; this end-to-end journey also covers a manually typed name.
	const model = page.locator(`[data-path="connections.${id}.model"]`);
	await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue(id);

	// 5. Name the connection and its model explicitly.
	await model.fill("second-model");
	await editor.locator(`[data-path="connections.${id}.label"]`).fill("second-model");
	await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue("second-model");

	// Test checks the selected connection without changing its draft fields.
	await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).click();
	await expect(page.locator(".connection-editor .discovery-note")).toContainText("Test passed in");

  // 6. THE STORED-KEY FIELD ROUND TRIP. What is typed is never read back: the field
  // returns saying a key is stored, and the key itself is not in it.
  const key = editor.locator(`[data-path="connections.${id}.api_key"]`);
  await expect(key).toHaveAttribute("type", "password");
  await key.fill("journey-secret-value");
  await key.press("Enter");
  await expect(page.locator('[data-action="save-connection"]')).toHaveCount(0);
  await page.locator('.settings-nav [data-id="about"]').click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  // The editor opens when its row is clicked; clicking an already-open one would
  // collapse it, which is the state this step needs to be in.
  if (!await page.locator(`[data-path="connections.${id}.api_key"]`).count()) {
    await page.locator(`[data-action="connection-toggle"][data-id="${id}"]`).click();
  }
  const reopened = page.locator(`[data-path="connections.${id}.api_key"]`);
  await expect(reopened).toBeVisible();
  await expect(reopened).toHaveValue("");
  await expect(reopened).not.toHaveAttribute("placeholder", /.*/);
  await expect(page.locator(".connection-editor .control-note").first()).toHaveText("stored");

  // 7. Put the built-in agent on it. Connections are assigned only in the agent
  // editor; the chat switcher now selects agents and never mutates one.
  await page.locator('.settings-nav [data-id="agents"]').click();
  await page.locator('#panel-agent').selectOption('agent_b');
  const agentSave = page.waitForResponse((response) => response.url().endsWith('/api/agents') && response.request().method() === 'POST');
  await page.locator('#panel-roles').evaluate((root, connection) => {
    root.querySelector('[data-field="connection"]').value = connection;
    root.querySelector('[data-action="save"]').click();
  }, id);
  expect((await agentSave).ok()).toBe(true);
  await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.agents.find((agent) => agent.name === 'agent_b').b).toBe(id);
  await toggleSettings(page);
  await expect(page.locator("#settings-page")).toBeHidden();
  await expect(page.locator(".shell-session-title")).toHaveText("agent_b");

  // 8. Send one message and see the answer on screen, from the controlled server.
  await page.locator("#chat-task").fill("journey: say something");
  await page.locator("#chat-send").click();
  await expect(page.locator("#chat-log")).toContainText("ui harness reply", { timeout: 60000 });
  await expect(page.locator(".chat-list-name.working")).toHaveCount(0);

  // 2ry W0: replay the reported sequence in the disposable harness before the
  // held-response case below: switch, answer, Settings > Chats, choose a face.
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="chats"]').click();
  await page.locator('button[data-action="config-choice"][data-path="chat.typeface"][data-value="Arial"]').click();
  await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.typeface).toBe("Arial");
  await expect(page.locator("[data-save-status]")).not.toContainText("Saving changes");
  await toggleSettings(page);

  // 9. Delete the chat through its four-entry row menu. This leaves the role
  // assignment as the first connection-removal refusal, just as Close did before
  // the tab strip was removed.
  const selected = page.locator(".chat-list-row.selected");
  const session = await selected.getAttribute("data-session");
  await selected.hover();
  await selected.locator(".chat-list-more").click();
  page.once("dialog", (dialog) => dialog.accept());
  await selected.locator(".chat-list-row-menu").getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.locator(`.chat-list-row[data-session="${session}"]`)).toHaveCount(0);

  // 10. Remove the connection, through the anchored confirmation (item 2l4), and
  // watch the row go.
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${id}"]`).click();
  const popover = page.locator(".confirm-popover");
  await expect(popover).toBeVisible();
  await popover.locator('[data-action="confirm-proceed"]').click();

  // 11. AND THE REFUSAL IS PART OF THE JOURNEY. Choosing this connection in the
  // agent editor bound agent_b to it, so removing it is refused - on the row
  // that was clicked, naming what to do about it (items 2mb (b), 2nc (c) and (d)).
  const row = page.locator(`.connection-row[data-id="${id}"]`);
  await expect(row).toContainText(/assigned to .* B role/);
  await expect(row).toContainText("Agents page");

  // 12. So he does what it says, and then it goes.
  await page.locator('.settings-nav [data-id="agents"]').click();
  await expect(page.locator("#panel-roles")).toBeVisible();
  // Any connection but the one being removed.
  await page.locator('#panel-agent').selectOption('agent_b');
  const role = page.locator('#panel-roles select[data-field="connection"]');
  const otherValue = () => role.locator("option").evaluateAll((options, connection) =>
    options.map((option) => option.value).find((value) => value && value !== connection) || "", id);
  await expect.poll(otherValue, { message: "alternate B-role connection option exists", timeout: 5000 }).not.toBe("");
  const other = await otherValue();
  const rebind = page.waitForResponse((response) => response.url().endsWith('/api/agents') && response.request().method() === 'POST');
  await page.locator('#panel-roles').evaluate((root, connection) => {
    root.querySelector('[data-field="connection"]').value = connection;
    root.querySelector('[data-action="save"]').click();
  }, other);
  expect((await rebind).ok()).toBe(true);
  await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.agents.find((agent) => agent.name === 'agent_b').b).toBe(other);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${id}"]`).click();
  await expect(page.locator(".confirm-popover")).toBeVisible();
  await page.locator('.confirm-popover [data-action="confirm-proceed"]').click();
  await expect(page.locator(`.connection-row[data-id="${id}"] .connection-summary`)).toHaveCount(0);
  await expect(page.locator(".connection-row")).toHaveCount(rowsBefore);
});

test("Entra credentials show account controls and configuration without defaults", async () => {
	const page = await harness.context.newPage();
	await page.route("**/api/credentials", async (route) => route.fulfill({
		status: 200,
		contentType: "application/json",
		body: JSON.stringify({ credentials: [{ name: "work-api", kind: "entra", origin: "https://api.example.test", stored_at: "2026-09-29T00:00:00Z", account: "someone@example.org", sign_in_needed: false }] }),
	}));
	await page.goto(`${harness.base}/chat`);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="shell"]').click();
	await expect(page.getByText("signed in as someone@example.org")).toBeVisible();
	await expect(page.locator('[data-action="credential-sign-in"][data-id="work-api"]')).toHaveText("Switch account");
	await expect(page.locator('[data-action="credential-device-code"][data-id="work-api"]')).toBeVisible();
	await expect(page.locator('[data-action="credential-sign-out"][data-id="work-api"]')).toBeVisible();
	await page.getByText("Add Microsoft Entra").click();
	for (const selector of ["#credential-entra-name", "#credential-entra-origin", "#credential-tenant", "#credential-client-id", "#credential-scopes"]) {
		await expect(page.locator(selector)).toHaveValue("");
	}
	const evidence = join(repo, "candidate", "rel-1.44.0-w7-entra", "settings-credentials.png");
	await mkdir(join(repo, "candidate", "rel-1.44.0-w7-entra"), { recursive: true });
	await page.screenshot({ path: evidence, fullPage: true });
	await page.close();
});

test("Phone shows all four wire states and follows presence without a reload", async () => {
	const page = await harness.context.newPage();
	let status = { state: "not paired" };
	await page.route("**/api/broker/status", async (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(status) }));
	const evidence = join(repo, "candidate", "rel-1.45.0-w1-phone");
	await mkdir(evidence, { recursive: true });
	await page.goto(`${harness.base}/chat`);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="shell"]').click();

	const cases = [[{ state: "not paired" }, "NOT PAIRED", "not-paired.png"], [{ state: "broker unreachable", paired_device: "phone", next_attempt_at: "2026-09-29T22:30:00Z", ended_reason: "network: dial refused" }, "PAIRED — BROKER UNREACHABLE", "broker-unreachable.png"], [{ state: "holding", paired_device: "phone" }, "PAIRED — HOLDING, PHONE NOT CONNECTED", "holding.png"], [{ state: "phone connected", paired_device: "phone", last_message_at: "2026-09-29T22:31:00Z" }, "PAIRED — PHONE CONNECTED", "phone-connected.png"]];
	for (const [next, label, file] of cases) {
		status = next;
		const renderedState = page.locator(".account-status").filter({ hasText: label }).first();
		await expect(renderedState).toBeVisible({ timeout: 5000 });
		await page.screenshot({ path: join(evidence, file), fullPage: true });
	}
	await expect(page.locator(".account-status").filter({ hasText: "last phone message 2026-09-29T22:31:00Z" }).first()).toBeVisible();
	await page.close();
});

test("telemetry consent is shared by Setup and About and persists both choices", async () => {
	const evidence = join(repo, "candidate", "rel-1.45.0-w2-telemetry");
	await mkdir(evidence, { recursive: true });
	const page = await harness.context.newPage();
	for (const enabled of [false, true]) {
		await page.goto(`${harness.base}/setup`);
		await page.locator('[data-action="later"]').click();
		await expect(page.locator("h1")).toHaveText("Send anonymous data to help improve Agent_b");
		await expect(page.locator("#setup-step")).toContainText("3 of 4");
		await expect(page.locator(".setup-note")).toHaveText("Only diagnostic data is sent — counts, durations and error classes. Never your chats, files or prompts.");
		const consent = page.locator('[data-action="toggle-telemetry"]');
		if ((await consent.getAttribute("aria-checked")) !== String(enabled)) await consent.click();
		if (!enabled) await page.screenshot({ path: join(evidence, "setup-anonymous-data.png"), fullPage: true });
		await page.locator('[data-action="telemetry-next"]').click();
		await expect(page.locator("h1")).toHaveText("Done");
		expect((await harness.getState()).config.telemetry.enabled).toBe(enabled);
	}
	await page.goto(`${harness.base}/chat`);
	await expect(page).toHaveURL(/\/chat/);
	await expect(page.locator("#setup")).toHaveCount(0);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="about"]').click();
	await expect(page.getByText("Send anonymous data to help improve Agent_b", { exact: true })).toBeVisible();
	await page.screenshot({ path: join(evidence, "settings-about-anonymous-data.png"), fullPage: true });
	await page.close();
});

// Item 2nc (c), carried into the journey: the confirmation belongs to its control, and
// the sheet scrolls. A popover that opens at the top of a scrolled sheet points at
// nothing, and the operator has to guess what he is confirming.
test("the confirmation popover stays with its control on a scrolled sheet", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  // Enough rows that the sheet scrolls.
  await page.setViewportSize({ width: 1250, height: 600 });
  for (let index = 0; index < 6; index++) {
    await page.locator('[data-action="add-connection"]').click();
  }
  // Scrolled by the control the operator would be reaching for, and read back in the
  // same evaluation, because a render restores this scroller's position.
  const scrolled = await page.evaluate(() => {
    const content = document.querySelector(".settings-content");
    const last = [...document.querySelectorAll('[data-action="remove-connection"]')].pop();
    last.scrollIntoView({ block: "end" });
    content.scrollTop = content.scrollHeight;
    return content.scrollTop;
  });
  expect(scrolled, "the sheet did not scroll, so this case proves nothing").toBeGreaterThan(0);

  await page.locator('.connection-row [data-action="remove-connection"]').last().click();
  const popover = page.locator(".confirm-popover");
  await expect(popover).toBeVisible();
  const geometry = await page.evaluate(() => {
    const anchor = [...document.querySelectorAll('[data-action="remove-connection"]')].pop().getBoundingClientRect();
    const box = document.querySelector(".confirm-popover").getBoundingClientRect();
    return {
      anchorTop: anchor.top, boxTop: box.top, boxBottom: box.bottom,
      visible: box.top >= 0 && box.bottom <= window.innerHeight,
    };
  });
  // Anchored: within a row's height of the thing it is asking about, and on screen
  // rather than scrolled out of it.
  expect(Math.abs(geometry.boxTop - geometry.anchorTop), JSON.stringify(geometry)).toBeLessThan(120);
  expect(geometry.visible, JSON.stringify(geometry)).toBe(true);
  const blocked = await popover.evaluate((open) => [...document.querySelectorAll("#settings-page button,#settings-page input,#settings-page textarea,#settings-page select,#settings-page summary")].filter((control) => {
    const box = control.getBoundingClientRect(), menuBox = open.getBoundingClientRect(), clip = document.querySelector(".settings-content").getBoundingClientRect(), x = box.left + box.width / 2, y = box.top + box.height / 2;
    const style = getComputedStyle(control);
    return control.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) && !control.disabled && style.display !== "none" && style.visibility !== "hidden" && box.left >= clip.left && box.top >= clip.top && box.right <= clip.right && box.bottom <= clip.bottom && !open.contains(control) && !(x >= menuBox.left && x <= menuBox.right && y >= menuBox.top && y <= menuBox.bottom) && !control.contains(document.elementFromPoint(x, y));
  }).map((control) => control.id || control.className || control.tagName));
  expect(blocked, `popover blocked outside control centres: ${blocked.join(", ")}`).toEqual([]);
  await page.locator(".settings-head").click(); await expect(popover).toHaveCount(0);
  await page.locator('.connection-row [data-action="remove-connection"]').last().click(); await page.locator('.settings-nav [data-id="about"]').click();
  await expect(popover).toHaveCount(0); await expect(page.locator('.settings-nav [data-id="about"]')).toHaveClass(/selected/);
  await page.locator('.settings-nav [data-id="connections"]').click(); await page.locator('.connection-row [data-action="remove-connection"]').last().click();
  await page.keyboard.press("Escape"); await expect(popover).toHaveCount(0);
  await page.locator('.connection-row [data-action="remove-connection"]').last().click(); await page.locator(".shell-session-title").click();
  await expect(popover).toHaveCount(0); await expect(page.locator(".shell-connection-menu")).toBeVisible(); await page.keyboard.press("Escape");
  await page.locator('.connection-row [data-action="remove-connection"]').last().click();
  await page.locator('.confirm-popover [data-action="confirm-cancel"]').click();
  await expect(popover).toHaveCount(0);
});

// Item 2nn's acceptance, cases 3, 5 and 6, on screen, because every one of the three
// is about what the operator sees and none of them can be proved from a response.

// 3: "Rename only, Save: saved, row shows the new label, no Test run, no refusal."
// This is the one he reported as a refusal and W0 found was a dead button: the save
// carried the disabled attribute from the last render and no request was ever made.
test("a rename alone saves, with no Test and no refusal", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  const posts = [];
  page.on("request", (request) => { if (request.method() === "POST" || request.method() === "PATCH") posts.push(request.url()); });
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  posts.length = 0;

  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed by hand");
  await editor.locator(`[data-path="connections.${id}.label"]`).press("Enter");
  await expect(page.locator('[data-action="save-connection"]')).toHaveCount(0);
  await expect(page.locator(`.connection-row[data-id="${id}"] .connection-summary`)).toContainText("renamed by hand");
  await expect(page.locator(".connection-refusal")).toHaveCount(0);
  expect(posts.some((url) => url.includes("/api/config")), `no save request was made: ${JSON.stringify(posts)}`).toBe(true);
  expect(posts.some((url) => url.includes("/probe")), "a rename ran a test").toBe(false);
  await page.close();
});

// 5: "During Test the bar is visible in a screenshot taken 100 ms after the click,
// with a phase line; the DOM never contains the bare text 'testing'." The address is
// a TEST-NET-1 one (RFC 5737), which nothing answers, so the walk is still running
// when the screenshot is taken.
test("the bar is on screen 100 ms after Test, and the bare word is not", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill("192.0.2.1:8080");

  await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).click();
  await page.waitForTimeout(100);
  // The viewport rather than the row: the list re-renders while the walk runs, and an
  // element screenshot of a node that was just replaced fails for a reason that has
  // nothing to do with what is being proved.
  const shot = await page.screenshot();
  expect(shot.length, "no screenshot was taken").toBeGreaterThan(0);
  const seen = await page.evaluate((connection) => {
    const seat = document.querySelector(`[data-probe-wait="${connection}"] .wait`);
    return {
      bar: !!seat,
      cells: seat ? seat.querySelectorAll(".wait-cell").length : 0,
      line: seat ? seat.querySelector(".wait-line").textContent.trim() : "",
      word: /(^|[^a-z])testing([^a-z]|$)/i.test(document.body.innerText),
    };
  }, id);
  expect(seen.bar, "there is no waiting element in the control that was pressed").toBe(true);
  expect(seen.cells).toBeGreaterThan(0);
  expect(seen.line, "the bar says nothing about what is being waited for").toContain("192.0.2.1:8080");
  expect(seen.word, "the bare word is still on screen").toBe(false);
  await page.close();
});

// 2qx replaces the two identity halves with the seven settings people change;
// the action line and collapsed Defaults remain beneath them.
test("the sheet reads as seven ordered fields above its actions and Defaults", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];

	const labels = async () => (await editor.locator(":scope > .connection-fields > .connection-primary > .setting-row > label").allTextContents()).filter(Boolean);
	expect(await labels()).toEqual(["name", "address", "key", "model", "context size", "thinking", "reads images"]);
  await expect(page.locator(".connection-defaults")).not.toHaveAttribute("open", /.*/);
  await expect(page.locator(".connection-defaults > summary")).toHaveText("Defaults");
  await expect(editor.locator(".connection-primary-actions button")).toHaveText(["Test", "Eval", "Recommended"]);

	// The empty new connection does not collapse or reorder the fields.
	await expect(page.locator(`.connection-editor [data-action="measure-connection"][data-id="${id}"]`)).toBeVisible();
	expect(await labels()).toEqual(["name", "address", "key", "model", "context size", "thinking", "reads images"]);
  await page.close();
});

// Item 2nq (c) and (d): HIS PICK SURVIVES TEST AND SAVE, and it is VISIBLE.
//
// "i select a different model and press test it goes back to MODEL. i save and it goes
// back to MODEL." W0 reproduced it on a stub and found the pick was never lost: the
// model picker rendered the SAVED value and never the draft, so every render after a
// choice — and Test is a render — drew the old value over his. The field lied.
test("the model he picks is the one the field shows, through Test and through Save", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
	const editor = page.locator(".connection-editor");
	const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
	const address = editor.locator(`[data-path="connections.${id}.base_url"]`);
	const addressSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
	const modelsListed = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${id}/models`));
	await address.fill(`127.0.0.1:${harness.modelPort}`);
	await address.press("Enter");
	await addressSaved;
	await modelsListed;

	let model = page.locator(`[data-path="connections.${id}.model"]`);
	if (await model.evaluate((node) => node.tagName === "SELECT")) {
		await model.selectOption("__type__");
		model = page.locator(`[data-path="connections.${id}.model"]`);
	}
	await model.fill("second-model");
  await expect(model, "the field forgot the pick as soon as it was made").toHaveValue("second-model");

  // Test again: the field still shows what he picked, not what is saved.
  await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).click();
  await expect(page.locator(".connection-editor .discovery-note")).toBeVisible();
  await expect(model, "Test drew the saved model over his pick").toHaveValue("second-model");

  // Enter writes it, and the field still shows it afterwards.
  await model.press("Enter");
  await expect(page.locator('[data-action="save-connection"]')).toHaveCount(0);
  await expect(model).toHaveValue("second-model");
  const saved = await page.evaluate(async () => (await (await fetch("/api/config")).json()).connections);
  const written = saved.find((connection) => connection.id === id);
  expect(written?.model, "the saved configuration does not hold the picked model").toBe("second-model");
  await page.close();
});

test("a held settings save releases the page with its draft still unsaved", async () => {
  test.setTimeout(30000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="chats"]').click();
  const before = (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.typeface;
  const draft = before === "Arial" ? "Verdana" : "Arial";
  let release;
  const held = new Promise((resolve) => { release = resolve; });
  await page.route("**/api/config", async (route) => { await held; await route.continue().catch(() => {}); });
  await page.locator(`button[data-action="config-choice"][data-path="chat.typeface"][data-value="${draft}"]`).click();
  await expect(page.locator("[data-save-status]")).toContainText("Saving changes");
  await expect(page.locator("[data-save-status]")).toContainText("no answer", { timeout: 11000 });
  await expect(page.locator(`button[data-path="chat.typeface"][data-value="${draft}"]`)).toHaveClass(/selected/);
  expect((await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.typeface).toBe(before);
  await page.locator('.settings-nav [data-id="about"]').click();
  await expect(page.locator(".settings-build-text")).toBeVisible();
  await toggleSettings(page);
  await expect(page.locator("#chat-log")).toBeVisible();
  release();
  await page.close();
});

// (d) and (e): a refused Save keeps every draft on screen and marks only the exact
// field the server refused. Item 2po deliberately removed the old row duplicate.
test("a refused save keeps the drafts and marks only its field", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed while refused");
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill("127.0.0.1:9");

  // The literal placeholder is not a model, so the server refuses to write it back —
  // which is exactly what kept it alive on the operator's disk.
  await page.evaluate((connection) => {
    const node = document.querySelector(`[data-path="connections.${connection}.model"]`);
    node.value = "";
    const option = document.createElement("option");
    option.value = "model";
    option.textContent = "model";
    node.append(option);
    node.value = "model";
    // Both, the way a real pick does: the input listener records the draft and the
    // change listener is what fills the rest from the chosen model.
    node.dispatchEvent(new Event("input", { bubbles: true }));
    node.dispatchEvent(new Event("change", { bubbles: true }));
  }, id);
  const modelField = editor.locator(`[data-path="connections.${id}.model"]`);
  await expect(modelField.locator("xpath=ancestor::div[contains(@class,'setting-row')]")).toHaveClass(/invalid/);
  await expect(editor.locator(".field-error")).toContainText("model is empty");
  const row = page.locator(`.connection-row[data-id="${id}"]`);
  await expect(row).not.toContainText("model is empty");
  // Every draft is still on screen: nothing was re-rendered back to the saved value.
  await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue("renamed while refused");
  await expect(editor.locator(`[data-path="connections.${id}.base_url"]`)).toHaveValue("127.0.0.1:9");
  await page.close();
});
