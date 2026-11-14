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
import { createServer } from "node:http";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { freePort, start } from "../ui-harness.mjs";
import { auditLayoutSnapshot, collectLayoutSnapshot } from "../layout-gate.mjs";
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
	test.setTimeout(120_000);
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

test.describe("setup wizard 2xz", () => {
	let wizardRoot;
	let wizardHarness;

	const standIn = async (options = {}) => {
		const state = { mode: options.mode || "ok", requests: 0 };
		const server = createServer(async (request, response) => {
			const key = request.headers.authorization || "";
			if (state.mode === "key" && key !== "Bearer accepted") { response.statusCode = 401; return void response.end(JSON.stringify({ error: "fixture key refused" })); }
			if (request.url === "/v1/models") {
				const models = state.mode === "none" ? [] : (options.models || ["wizard-model"]);
				return void response.end(JSON.stringify({ data: models.map((id) => ({ id, context_length: options.window || 0 })) }));
			}
			if (request.url === "/props") return void response.end(JSON.stringify(options.window ? { default_generation_settings: { n_ctx: options.window } } : {}));
			if (request.url !== "/v1/chat/completions") { response.statusCode = 404; return void response.end(JSON.stringify({ error: "fixture route" })); }
			state.requests += 1;
			if (state.mode === "empty") return void response.end(JSON.stringify({ choices: [{ message: { role: "assistant", content: "" } }] }));
			response.end(JSON.stringify({ choices: [{ message: { role: "assistant", content: "wizard answer" } }] }));
		});
		await new Promise((done) => server.listen(options.port || 0, "127.0.0.1", done));
		return { url: `http://127.0.0.1:${server.address().port}`, state, close: () => { server.closeAllConnections?.(); return new Promise((done) => server.close(done)); } };
	};

	const openSetup = async (page) => {
		await page.goto(`${wizardHarness.base}/setup?from=settings`);
		await expect(page.getByRole("heading", { name: "Set up a model" })).toBeVisible();
	};

	const connect = async (page, url, { key = "", model = "" } = {}) => {
		await page.getByLabel("Address").fill(url);
		if (key || model) {
			await page.getByRole("button", { name: /More options/ }).click();
			if (key) await page.getByLabel("API key").fill(key);
			if (model) await page.getByLabel("Model").fill(model);
		}
		await page.getByRole("button", { name: "Connect" }).click();
	};

	test.beforeAll(async () => {
		test.setTimeout(300_000);
		wizardRoot = await mkdtemp(join(tmpdir(), "agentb-setup-wizard-"));
		const exe = join(wizardRoot, "Agent_b.exe");
		await run(join(repo, ".tools", "go", "bin", "go.exe"), ["build", "-o", exe, "./cmd/harness"], { cwd: repo, windowsHide: true });
		wizardHarness = await start({ exe, appRoot: repo, data: join(wizardRoot, "data") });
	});

	test.afterAll(async () => {
		await wizardHarness?.stop();
		if (wizardRoot) removeTreeWithinAllowedRoots(wizardRoot, [tmpdir()], "setup wizard Playwright cleanup");
	});

	test("2xz CHECK 2 connects, saves the reported window, finishes and answers", async () => {
		const model = await standIn({ window: 32768 }); const page = await wizardHarness.context.newPage();
		try {
			await openSetup(page); await connect(page, model.url); await expect(page.getByText("32,768", { exact: true })).toBeVisible();
			await page.getByRole("button", { name: "Next" }).click(); await page.getByRole("button", { name: "Finish" }).click(); await page.waitForURL(/\/chat/);
			const state = await wizardHarness.getState(); expect(state.config.connections.find((item) => item.base_url === model.url)?.context?.n_ctx).toBe(32768); expect(model.state.requests).toBe(1);
			await page.locator("#chat-task").fill("setup wizard answer"); await page.locator("#chat-send").click(); await expect(page.locator("#chat-log")).toContainText("ui harness reply");
		} finally { await page.close(); await model.close(); }
	});

	test("2xz CHECK 3 requires a positive suggested context when none is reported", async () => {
		const model = await standIn(); const page = await wizardHarness.context.newPage();
		try {
			await openSetup(page); await connect(page, model.url); const context = page.getByLabel("Context size"); await expect(context).toHaveValue("8192");
			await context.fill("0"); await expect(page.getByRole("button", { name: "Next" })).toHaveCount(0); await context.fill(""); await expect(page.getByRole("button", { name: "Next" })).toHaveCount(0); await context.fill("16384"); await expect(page.getByRole("button", { name: "Next" })).toBeVisible();
		} finally { await page.close(); await model.close(); }
	});

	test("2xz CHECK 4 waits for a model pick when the server lists three", async () => {
		const model = await standIn({ window: 32768, models: ["one", "two", "three"] }); const page = await wizardHarness.context.newPage();
		try {
			await openSetup(page); await connect(page, model.url); const picker = page.getByLabel("Model"); await expect(picker.locator("option")).toHaveText(["", "one", "two", "three"]); await expect(page.getByRole("button", { name: "Next" })).toHaveCount(0); await picker.selectOption("two"); await expect(page.getByRole("button", { name: "Next" })).toBeVisible();
		} finally { await page.close(); await model.close(); }
	});

	for (const scenario of [
		{ name: "closed port", line: "Nothing answered at that address.", setup: async () => { const port = await freePort(); return { url: `http://127.0.0.1:${port}`, repair: async () => standIn({ window: 32768, port }) }; } },
		{ name: "refused key", line: "The server refused the key.", setup: async () => { const server = await standIn({ mode: "key", window: 32768 }); return { url: server.url, key: "wrong", server, repair: async () => { server.state.mode = "ok"; return server; } }; } },
		{ name: "no model", line: "No model is loaded on that server.", setup: async () => { const server = await standIn({ mode: "none" }); return { url: server.url, server, repair: async () => { server.state.mode = "ok"; return server; } }; } },
		{ name: "empty answer", line: "The model did not answer.", setup: async () => { const server = await standIn({ mode: "empty", window: 32768 }); return { url: server.url, server, repair: async () => { server.state.mode = "ok"; return server; } }; } },
	]) test(`2xz CHECK 5 ${scenario.name} shows its fixed line and retries`, async () => {
		test.setTimeout(240_000); const fixture = await scenario.setup(); const page = await wizardHarness.context.newPage(); let repaired;
		try {
			await openSetup(page); await connect(page, fixture.url, { key: fixture.key }); await expect(page.getByText(scenario.line, { exact: true })).toBeVisible(); await expect(page.getByText("details", { exact: true })).toBeVisible(); repaired = await fixture.repair();
			if (repaired.url !== fixture.url) { await page.getByRole("button", { name: "Back" }).click(); await page.getByLabel("Address").fill(repaired.url); await page.getByRole("button", { name: "Connect" }).click(); } else await page.getByRole("button", { name: "Try again" }).click();
			await expect(page.getByRole("button", { name: "Next" }), "repair did not complete all checks").toBeVisible({ timeout: 90_000 });
		} finally { await page.close(); if (repaired && repaired !== fixture.server) await repaired.close(); await fixture.server?.close(); }
	});

	test("2xz CHECK 6 closes every screen and cancels a download", async () => {
		const model = await standIn({ window: 32768 });
		for (const screen of [1, 2, 3]) {
			const page = await wizardHarness.context.newPage(); await openSetup(page); if (screen > 1) await connect(page, model.url); if (screen > 2) await page.getByRole("button", { name: "Next" }).click(); await page.getByTitle("Close setup").click(); await page.waitForURL(/\/chat\?setup=skip/); await expect(page.locator("#chat-task")).toBeVisible(); await page.goto(`${wizardHarness.base}/chat#settings/connections`); await expect(page.getByRole("button", { name: "Open setup guide" })).toBeVisible(); await page.close();
		}
		const page = await wizardHarness.context.newPage(); let cancelled = false;
		await page.route("**/api/local-detection", (route) => route.fulfill({ json: { system_memory_bytes: 16 * 2 ** 30, gpus: [], accelerators: {} } }));
		await page.route("**/api/model-install", (route) => route.request().method() === "DELETE" ? (cancelled = true, route.fulfill({ json: { running: false, phase: "cancelled" } })) : route.request().method() === "POST" ? route.fulfill({ status: 202, json: { running: true } }) : route.fulfill({ json: { catalog: { models: [{ id: "local", label: "Local", min_gib: 1, artifact: { bytes: 100 } }] }, state: { running: true, total_bytes: 100, downloaded_bytes: 10 } } }));
		await openSetup(page); await page.getByRole("button", { name: "Install" }).click(); await page.getByTitle("Close setup").click(); await page.waitForURL(/\/chat\?setup=skip/); expect(cancelled).toBe(true); await page.close(); await model.close();
	});

	test("2xz CHECK 7 keeps setup to its three screens and Settings keeps advanced controls", async () => {
		const model = await standIn({ window: 32768 }); const page = await wizardHarness.context.newPage();
		try {
			await openSetup(page); await expect(page.locator("body")).not.toContainText(/Skip|Later|Open Chat|Evaluation Harness|separate Windows account/); await connect(page, model.url); await page.getByRole("button", { name: "Next" }).click(); await expect(page.getByRole("heading", { name: "Agent_b is ready" })).toBeVisible(); await expect(page.locator(".setup-main button.setup-primary")).toHaveText("Finish");
			await page.goto(`${wizardHarness.base}/chat#settings/connections`); await page.locator('.model-row[data-id="ui"]').click(); await expect(page.getByRole("button", { name: "Eval", exact: true }).first()).toBeVisible(); await page.locator('.settings-nav [data-id="shell"]').click(); await expect(page.locator(".settings-content")).toContainText("Experimental");
		} finally { await page.close(); await model.close(); }
	});

	test("2xz CHECK 8 captures every designed state at three sizes", async () => {
		test.setTimeout(240_000); const captures = join(repo, "logs", "evidence", "rel-1.62.204", "captures"); await mkdir(captures, { recursive: true }); const sizes = [[304, 720], [1280, 860], [1920, 1080]];
		const capture = async (page, state) => { for (const [width, height] of sizes) { await page.setViewportSize({ width, height }); expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".setup-wizard")), `${state} ${width}`).toEqual([]); await page.screenshot({ path: join(captures, `${state}-${width}.png`) }); } };
		const choose = await wizardHarness.context.newPage(); await openSetup(choose); await capture(choose, "choose"); await choose.getByRole("button", { name: /More options/ }).click(); await capture(choose, "more"); await choose.close();
		const installing = await wizardHarness.context.newPage(); await installing.route("**/api/local-detection", (route) => route.fulfill({ json: { system_memory_bytes: 16 * 2 ** 30, gpus: [], accelerators: {} } })); await installing.route("**/api/model-install", (route) => route.request().method() === "POST" ? route.fulfill({ status: 202, json: { running: true } }) : route.fulfill({ json: { catalog: { models: [{ id: "local", label: "Example-9B", min_gib: 1, artifact: { bytes: 6 * 2 ** 30 } }] }, state: { running: true, total_bytes: 6 * 2 ** 30, downloaded_bytes: 3.7 * 2 ** 30 } } })); await openSetup(installing); await installing.getByRole("button", { name: "Install" }).click(); await capture(installing, "installing"); await installing.close();
		const checking = await wizardHarness.context.newPage(); await checking.route("**/api/connections/setup-model/models", () => {}); await openSetup(checking); await checking.getByRole("button", { name: "Connect" }).click(); await capture(checking, "checking"); await checking.close();
		const askServer = await standIn(); const ask = await wizardHarness.context.newPage(); await openSetup(ask); await connect(ask, askServer.url); await expect(ask.getByLabel("Context size")).toBeVisible(); await capture(ask, "context-suggested"); await ask.close(); await askServer.close();
		const bad = await wizardHarness.context.newPage(); await bad.route("**/api/connections/setup-model/models", (route) => route.fulfill({ json: { models: [], message: "fixture has no model" } })); await openSetup(bad); await bad.getByRole("button", { name: "Connect" }).click(); await expect(bad.getByText("No model is loaded on that server.", { exact: true })).toBeVisible(); await capture(bad, "failed"); await bad.close();
		const doneServer = await standIn({ window: 32768 }); const done = await wizardHarness.context.newPage(); await openSetup(done); await connect(done, doneServer.url); await done.getByRole("button", { name: "Next" }).click(); await capture(done, "ready"); await done.close(); await doneServer.close();
	});
});

test("generic skill-folder add shows the complete Hermes-layout preview before writing", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="skills"]').click();
	await page.route("**/api/folder-picker", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ path: hermesHome }) }));
	await page.getByRole("button", { name: "Add skill", exact: true }).click();
	await expect(page.locator(".skill-add-preview")).toBeVisible();
	for (const name of ["MEMORY.md", "USER.md", "portable", "FIRST_KEY", "SECOND_KEY"]) await expect(page.locator("#settings-page")).toContainText(name);
	for (const omitted of ["SOUL.md", "cron", "sessions"]) await expect(page.locator("#settings-page")).not.toContainText(omitted);
	await expect(page.getByRole("button", { name: "Add", exact: true })).toBeVisible();
	const evidence = join(repo, "logs", "evidence", "rel-1.58.0"); await mkdir(evidence, { recursive: true });
	await page.screenshot({ path: join(evidence, "hermes-preview.png"), fullPage: true });
	await page.close();
});

test.afterAll(async () => {
	test.setTimeout(120_000);
  await harness?.stop();
  if (root) {
		const deadline = Date.now() + 20_000;
		for (;;) {
			try { removeTreeWithinAllowedRoots(root, [tmpdir()], "settings-journey Playwright cleanup"); break; }
			catch (error) { if (error?.code !== "EPERM" || Date.now() >= deadline) throw error; await new Promise((resolve) => setTimeout(resolve, 100)); }
		}
	}
});

test("Settings is a centred bounded window over the drawn chat 2sy", async () => {
	const page = await harness.context.newPage();
	for (const width of [1280, 1920]) {
		await page.setViewportSize({ width, height: 900 }); await page.goto(`${harness.base}/chat`); await toggleSettings(page);
		await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
		const box = await page.evaluate(() => { const panel = document.querySelector("#settings-page").getBoundingClientRect(), strip = document.querySelector("#app-shell").getBoundingClientRect(); return { width: panel.width, height: panel.height, dx: Math.abs(panel.left + panel.width / 2 - innerWidth / 2), dy: Math.abs(panel.top + panel.height / 2 - (strip.bottom + (innerHeight - strip.bottom) / 2)), chat: document.querySelector("#chat-log").checkVisibility(), list: document.querySelector(".chat-list-panel").checkVisibility(), backdrop: document.querySelector("#settings-backdrop")?.checkVisibility() || false }; });
		expect(box).toMatchObject({ width: width * .8, height: 720, chat: true, list: true, backdrop: true }); expect(box.dx).toBeLessThanOrEqual(1); expect(box.dy).toBeLessThanOrEqual(16);
		await page.screenshot({ path: join(repo, "test-results", `2sy-settings-${width}.png`), fullPage: true }); await page.getByTitle("Close").click();
		await expect(page.locator("#settings-page")).toBeHidden();
	}
	await page.close();
});

test("Settings fills narrow windows and its X closes to prior focus 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`);
	for (const width of [304, 600]) for (const size of [9, 12, 32]) {
		await page.setViewportSize({ width, height: 700 }); await page.locator(".chat-list-menu-button").focus(); await page.evaluate((value) => document.documentElement.style.setProperty("--chat-size", `${value}px`), size); await page.evaluate(() => document.dispatchEvent(new CustomEvent("settings.open")));
		await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
		const geometry = await page.locator("#settings-page").evaluate((panel) => { const box = panel.getBoundingClientRect(), strip = document.querySelector("#app-shell").getBoundingClientRect(), close = panel.querySelector('[data-action="close"]').getBoundingClientRect(), head = panel.querySelector(".settings-head").getBoundingClientRect(), content = panel.querySelector(".settings-content").getBoundingClientRect(), group = panel.querySelector(".settings-group").getBoundingClientRect(); return { left: box.left, right: innerWidth - box.right, top: box.top - strip.bottom, bottom: innerHeight - box.bottom, contentLeft: content.left - box.left, contentRight: box.right - content.right, groupLeft: group.left - box.left, groupRight: box.right - group.right, overflow: document.documentElement.scrollWidth - innerWidth, closeWhole: close.left >= head.left && close.right <= head.right && close.top >= head.top && close.bottom <= head.bottom }; });
		expect({ ...geometry, groupRight: undefined }).toEqual({ left: 0, right: 0, top: 0, bottom: 0, contentLeft: 1, contentRight: 1, groupLeft: 13, groupRight: undefined, overflow: 0, closeWhole: true }); expect(geometry.groupRight).toBeGreaterThanOrEqual(13); await page.screenshot({ path: join(repo, "test-results", `2sy-settings-${width}-${size}px.png`), fullPage: true }); await page.getByTitle("Close").press("Enter"); await expect(page.locator(".chat-list-menu-button")).toBeFocused();
	}
	await page.close();
});

test("only the dimmed chat closes Settings; the strip does not 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await toggleSettings(page); await expect(page.locator(".shell-settings")).toHaveCount(0);
	const strip = await page.locator("#app-shell").boundingBox(); await page.mouse.click(strip.x + 200, strip.y + 12); await page.mouse.dblclick(strip.x + 220, strip.y + 12); await page.mouse.move(strip.x + 250, strip.y + 12); await page.mouse.down(); await page.mouse.move(strip.x + 300, strip.y + 12); await page.mouse.up(); await expect(page.locator("#settings-page")).toBeVisible();
	await page.locator("#settings-backdrop").click({ position: { x: 4, y: 4 } }); await expect(page.locator("#settings-page")).toBeHidden(); await page.close();
});

test("Chats shows only profile reading approval context and delete 2sb", async () => {
	test.setTimeout(120_000);
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	await page.evaluate(() => document.dispatchEvent(new CustomEvent("settings.open", { detail: { section: "chats" } })));
	const initial = await (await page.request.get(`${harness.base}/api/state`)).json();
	if ((initial.profiles?.names || []).length <= 1) for (const section of ["chats", "profiles", "notifications"]) {
		await page.locator(`.settings-nav [data-id="${section}"]`).click();
		await expect(page.locator("[data-profile-select]")).toHaveCount(0);
	}
	await page.getByTitle("Close").click();

	const original = initial.profiles?.active || "Default";
	const second = "Second fixture";
	const mutationHeaders = { "X-AgentB-Mutation-Token": await page.locator('meta[name="agentb-mutation-token"]').getAttribute("content") };
	if (!(initial.profiles?.names || []).includes(second)) {
		const created = await page.request.post(`${harness.base}/api/profiles`, { headers: mutationHeaders, data: { action: "create", name: second } });
		expect(created.ok(), `${created.status()} ${await created.text()}`).toBe(true);
	}
	await page.reload();
	await page.evaluate(() => document.dispatchEvent(new CustomEvent("settings.open", { detail: { section: "chats" } })));
	for (const section of ["chats", "profiles", "notifications"]) {
		await page.locator(`.settings-nav [data-id="${section}"]`).click();
		await expect(page.locator("[data-profile-select]")).toHaveCount(1);
	}
	await page.locator('.settings-nav [data-id="chats"]').click();
	await expect(page.locator("[data-profile-select]")).toHaveValue(original);
	await expect(page.locator('.settings-nav [data-id="plan"]')).toBeVisible();
	const text = (await page.locator(".settings-content").innerText()).toLowerCase();
	for (const absent of ["root path", "tabs", "repeat limit", "queue depth", "reflection floor", "max concurrent", "cycle window"]) expect(text).not.toContain(absent);
	expect(await page.locator(".settings-subhead").allTextContents()).toEqual(["Reading", "Context", "Delete"]);
	await expect(page.locator(".settings-subhead-note")).toHaveCount(0);
	const size = page.locator('[data-path="chat.text_size"]');
	for (const invalid of ["8", "33"]) {
		await size.fill(invalid); await size.press("Tab");
		await expect(page.locator(".field-error")).toContainText("9 to 32");
	}
	await size.fill("16"); await size.press("Tab");
	await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.text_size).toBe(16);

	const configured = { run: { cycle_window: 7, queue_depth: 3, max_concurrent: 2 }, reflection: { floor: { model_calls: 5, tool_calls: 4 } }, chat: { hidden_surfaces: ["plan"], typeface: "Absent Font Fixture" } };
	const patched = await page.request.post(`${harness.base}/api/config`, { headers: mutationHeaders, data: configured });
	expect(patched.ok()).toBe(true);
	await expect(page.locator('[data-menu-path="chat.typeface"]')).toContainText("Absent Font Fixture · missing");
	const evidence = join(repo, "logs", "evidence", "rel-1.62.33"); await mkdir(evidence, { recursive: true });
	const stabilizeMenu = async (path) => page.waitForFunction((menuPath) => {
		const anchor = document.querySelector(`[data-menu-path="${menuPath}"]`), menu = anchor.nextElementSibling, key = `__stable_${menuPath}`;
		if (menu.hidden) { menu.hidden = false; anchor.setAttribute("aria-expanded", "true"); window[key] = null; return false; }
		if (window[key]?.menu !== menu) { window[key] = { menu, at: performance.now() }; return false; }
		return performance.now() - window[key].at >= 250;
	}, path);
	await expect.poll(async () => page.locator('.font-menu .setting-menu [role="option"]').count()).toBeGreaterThan(100);
	await stabilizeMenu("chat.typeface");
	const faceWidths = await page.locator('.font-menu .setting-menu').evaluate((menu) => {
		const width = (node) => { const range = document.createRange(); range.selectNodeContents(node); return range.getBoundingClientRect().width; };
		const probe = (text, family, source) => { const node = document.createElement("span"); node.textContent = text; node.style.cssText = `position:fixed;visibility:hidden;white-space:pre;font:${getComputedStyle(source).font};font-family:${family}`; document.body.append(node); const value = width(node); node.remove(); return value; };
		const rows = [...menu.querySelectorAll('[role="option"]')].map((button) => {
			const label = button.querySelector("strong"), name = label.textContent;
			return { name, drawn: width(label), own: probe(name, `"${name.replaceAll('"', '')}",sans-serif`, label), base: probe(name, '"IBM Plex Sans",sans-serif', label) };
		}).filter((row) => !["Absent Font Fixture", "IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic"].includes(row.name) && Math.abs(row.own - row.base) > 1);
		const required = ["Arial Narrow", "Arial Black"].map((name) => rows.find((row) => row.name === name));
		return [...required, ...rows.filter((row) => !["Arial Narrow", "Arial Black"].includes(row.name))].filter(Boolean).slice(0, 10);
	});
	console.log("R1 face widths", JSON.stringify(faceWidths));
	expect(faceWidths.map(({ name }) => name)).toEqual(expect.arrayContaining(["Arial Narrow", "Arial Black"]));
	expect(faceWidths).toHaveLength(10);
	for (const row of faceWidths) { expect(Math.abs(row.drawn - row.base), `${row.name} is visibly not the default face`).toBeGreaterThan(1); expect(row.drawn, `${row.name} uses its own drawn width`).toBeCloseTo(row.own, 0); }
	await page.waitForFunction(() => {
		const anchor = document.querySelector('[data-menu-path="chat.typeface"]'), menu = anchor.nextElementSibling;
		if (!menu.hidden) { menu.hidden = true; anchor.removeAttribute("aria-expanded"); window.__fontMenuClosed = null; return false; }
		if (window.__fontMenuClosed?.menu !== menu) { window.__fontMenuClosed = { menu, at: performance.now() }; return false; }
		return performance.now() - window.__fontMenuClosed.at >= 250;
	});
	await stabilizeMenu("chat.text_color");
	await expect(page.locator('.colour-menu .setting-menu')).toBeVisible();
	const expectedColours = [[232,238,244],[255,255,255],[255,107,107],[255,159,67],[255,209,102],[60,240,138],[77,223,247],[108,168,255],[199,146,234],[255,133,194]];
	const colourRows = page.locator('.colour-menu .setting-menu [role="option"] strong');
	await expect(colourRows).toHaveCount(10);
	expect(await colourRows.evaluateAll((nodes) => nodes.map((node) => getComputedStyle(node).color))).toEqual(expectedColours.map((colour) => `rgb(${colour.join(", ")})`));
	await page.evaluate(() => { const anchor = document.querySelector('[data-menu-path="chat.text_color"]'); anchor.nextElementSibling.hidden = true; anchor.removeAttribute("aria-expanded"); });
	for (const viewport of [{ name: "small", width: 304, height: 254 }, { name: "medium", width: 1280, height: 860 }, { name: "large", width: 1920, height: 1080 }]) for (const pixels of [9, 32]) {
		await page.setViewportSize(viewport); await page.locator('[data-path="chat.text_size"]').fill(String(pixels)); await page.locator('[data-path="chat.text_size"]').evaluate((node) => node.blur());
		await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.text_size).toBe(pixels);
		await expect(page.locator('[data-path="chat.text_size"]')).toHaveValue(String(pixels));
		await expect.poll(async () => page.locator('.settings-nav button').first().evaluate((node) => getComputedStyle(node).fontSize)).toBe("12px");
		for (const path of ["chat.typeface", "chat.text_color"]) {
			const picture = join(evidence, `2sb-${viewport.name}-${pixels}px-${path.split(".")[1]}.png`);
			await expect(async () => {
				await page.evaluate((menuPath) => { const anchor = document.querySelector(`[data-menu-path="${menuPath}"]`); anchor.scrollIntoView({ block: "nearest" }); anchor.nextElementSibling.hidden = false; anchor.setAttribute("aria-expanded", "true"); }, path);
				await page.locator(`[data-menu-path="${path}"] + .setting-menu`).screenshot({ path: picture });
			}).toPass();
			await page.evaluate((menuPath) => { const anchor = document.querySelector(`[data-menu-path="${menuPath}"]`); anchor.nextElementSibling.hidden = true; anchor.removeAttribute("aria-expanded"); }, path);
		}
	}
	await page.setViewportSize({ width: 1280, height: 860 });
	await page.locator('[data-menu-path="approval.mode"]').click();
	await expect(page.locator('.approval-menu .setting-menu [role="option"]')).toHaveCount(3);
	await expect(page.locator('.approval-menu .setting-menu [role="option"] span')).toHaveCount(3);
	await page.locator('[data-menu-path="approval.mode"]').click();
	for (const mode of ["mutating", "all", "boundary-only"]) {
		await page.locator('[data-menu-path="approval.mode"]').click();
		await page.locator(`button[data-action="config-choice"][data-path="approval.mode"][data-value="${mode}"]`).click();
		await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.approval.mode).toBe(mode);
	}
	await page.locator('[data-menu-path="chat.text_color"]').click();
	await page.locator('button[data-action="config-choice"][data-path="chat.text_color"][data-value="pink"]').click();
	await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.text_color).toBe("pink");
	const preserved = (await (await page.request.get(`${harness.base}/api/state`)).json()).config;
	expect({ cycle: preserved.run.cycle_window, queue: preserved.run.queue_depth, concurrent: preserved.run.max_concurrent, model: preserved.reflection.floor.model_calls, tool: preserved.reflection.floor.tool_calls }).toEqual({ cycle: 7, queue: 3, concurrent: 2, model: 5, tool: 4 });
	await expect(page.locator('.settings-nav [data-id="plan"]')).toBeVisible();

	await page.locator("[data-profile-select]").selectOption(second);
	await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).profiles.active).toBe(second);
	await expect(page.locator("[data-profile-select]")).toHaveValue(second);
	await page.locator("[data-profile-select]").selectOption(original);
	await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).profiles.active).toBe(original);
	let refusedSwitch;
	await page.route("**/api/profiles", async (route) => {
		refusedSwitch = route.request().postDataJSON();
		await route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ error: "stop the run first", field: "profiles" }) });
	});
	await page.locator("[data-profile-select]").selectOption(second);
	await expect(page.locator(".settings-group-error")).toContainText(/stop the run first/i);
	await expect(page.locator("[data-profile-select]")).toHaveValue(original);
	expect(refusedSwitch).toEqual({ action: "switch", name: second });
	await page.getByTitle("Close").click();
	await page.close();
});

test("Settings traps focus and blocks keys from the chat behind it 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await page.locator("#chat-task").fill("held"); await toggleSettings(page);
	const controls = page.locator('#settings-page button:not([disabled]),#settings-page input:not([disabled]),#settings-page textarea:not([disabled]),#settings-page select:not([disabled]),#settings-page [tabindex="0"]'); const count = await controls.count(); await controls.nth(count - 1).focus(); await page.keyboard.press("Tab"); await expect(controls.first()).toBeFocused(); await page.keyboard.type("x"); await expect(page.locator("#chat-task")).toHaveValue("held"); await page.close();
});

test("a live run keeps drawing beneath Settings 2sy", async () => {
	// The full loaded run has taken 59 s to deliver a 20 s controlled stream;
	// this 1.6 s stream still has to finish, but the suite's 30 s cap is not it.
	test.setTimeout(120_000);
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); const original = await page.locator(".chat-list-row.selected").getAttribute("data-session"); await page.locator(".chat-list-new").click(); await expect(page.locator(".chat-list-row.selected")).not.toHaveAttribute("data-session", original); const streamed = await page.locator(".chat-list-row.selected").getAttribute("data-session"); await page.locator("#chat-task").fill("stream behind Settings"); await page.locator("#chat-send").click();
	await expect(page.locator("#chat-log")).toContainText("ui harness "); await toggleSettings(page); await expect(page.locator(".chat-list-name.working")).toBeVisible();
	const before = (await page.locator("#chat-log").innerText()).length; await page.screenshot({ path: join(repo, "test-results", "2sy-settings-streaming.png"), fullPage: true });
	await expect(page.locator("#chat-log")).toContainText("ui harness reply"); await expect.poll(async () => (await page.locator("#chat-log").innerText()).length).toBeGreaterThan(before); await expect(page.locator(".chat-list-name.working")).toHaveCount(0); await page.getByTitle("Close").click(); await page.locator(`.chat-list-row[data-session="${original}"] .chat-list-name`).click(); const streamRow = page.locator(`.chat-list-row[data-session="${streamed}"]`); await streamRow.hover(); await streamRow.locator(".chat-list-more").click(); await streamRow.locator(".chat-list-row-menu").getByRole("button", { name: "Delete", exact: true }).click(); await streamRow.locator(".chat-list-delete-confirm").getByRole("button", { name: "Delete", exact: true }).click(); await expect(streamRow).toHaveCount(0); await page.close();
});

test("the setup document uses an X, lands on chat, and reopens from Settings 2sy", async () => {
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await toggleSettings(page); await page.locator('[data-action="open-setup"]').click();
	await expect(page).toHaveURL(/\/setup\?from=settings/); await expect(page.locator(".setup-x")).toHaveText("×"); await expect(page.locator(".setup-x")).toHaveAttribute("title", "Close setup"); await expect(page.locator("body")).not.toContainText("⚙");
	await page.locator(".setup-x").click(); await expect(page).toHaveURL(/\/chat\?setup=skip/); await expect(page.locator("#chat-task")).toBeVisible(); await toggleSettings(page); await page.locator('.settings-nav [data-id="connections"]').click(); await expect(page.locator('[data-action="open-setup"]')).toBeVisible(); await page.close();
});

test("the settings journey: type a host, pick a model, Test, save, chat, delete, remove", async () => {
  // Under eight CPU burners the label node was detached 553 times by its own
  // redraw until this journey's 180 s bound fired. The atomic input below
  // exercises that redraw once instead of waiting longer for a stale node.
  test.setTimeout(180000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();

  // 1. Open Settings and its Connections section, on screen.
  await toggleSettings(page);
  await expect(page.locator("#settings-page")).toBeVisible();
  await page.locator('.settings-nav [data-id="connections"]').click();
  const rowsBefore = await page.locator(".model-row").count();

  // 2. Add a connection. The row appears and its editor is open.
  await page.locator('[data-action="add-connection"]').click();
  await expect(page.locator(".model-row")).toHaveCount(rowsBefore + 1);
  const editor = page.locator(".model-editor");
  const serverEditor = page.locator(".server-editor");
  await expect(editor).toBeVisible();
  const path = await editor.locator("[data-path$='.model']").getAttribute("data-path");
  const id = path.split(".")[1];
	const typeDraft = (locator, value) => locator.evaluate((node, next) => {
		node.value = next;
		node.dispatchEvent(new Event("input", { bubbles: true }));
	}, value);

  // 3. TYPE A HOST. He types where his server is; the port he typed is the one
  // discovery tries first (item 2nb (a)).
  await typeDraft(serverEditor.locator('[data-server-field="base_url"]'), `http://127.0.0.1:${harness.modelPort}`);

	// 4. Name the model this connection should test. Model-list discovery has its own
	// focused journey; this end-to-end journey also covers a manually typed name.
	const model = page.locator(`[data-path="connections.${id}.model"]`);
	await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue(id);

	// 5. Name the connection and its model explicitly.
	await typeDraft(model, "second-model");
	await editor.locator(`[data-path="connections.${id}.label"]`).evaluate((node) => {
		node.value = "second-model";
		node.dispatchEvent(new Event("input", { bubbles: true }));
		node.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
	});
	await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue("second-model");
	await expect.poll(async () => (await harness.getState()).config.connections.find((connection) => connection.id === id)?.label, { timeout: 60000 }).toBe("second-model");

	// Test checks the selected connection without changing its draft fields.
	// The loaded page can receive a fresh snapshot between pointerdown and click,
	// which detached this button 234 times. Dispatch the click to the current
	// button; the journey already proved above that the draft finished saving.
	await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).dispatchEvent("click");
	await expect(page.locator(".connection-editor .discovery-note")).toContainText("Test passed in", { timeout: 60000 });

  // 6. THE STORED-KEY FIELD ROUND TRIP. What is typed is never read back: the field
  // returns saying a key is stored, and the key itself is not in it.
  const key = serverEditor.locator('[data-server-field="api_key"]');
  await expect(key).toHaveAttribute("type", "password");
  await key.fill("journey-secret-value");
  await key.press("Enter");
  await expect(page.locator('[data-action="save-connection"]')).toHaveCount(0);
  await page.locator('.settings-nav [data-id="about"]').click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  // The editor opens when its row is clicked; clicking an already-open one would
  // collapse it, which is the state this step needs to be in.
  if (!await page.locator('[data-server-field="api_key"]').count()) {
    await page.locator(`[data-action="server-toggle"]`).last().click();
  }
  const reopened = page.locator('[data-server-field="api_key"]').last();
  await expect(reopened).toBeVisible();
  await expect(reopened).toHaveValue("");
  await expect(reopened).toHaveAttribute("placeholder", "stored");

  // 7. A connection with no named profile uses agent_b, and the chat switcher
  // selects that connection without mutating another one.
  await expect(editor.locator(`[data-path="connections.${id}.agent_profile"]`)).toHaveValue("");
  await toggleSettings(page);
  await expect(page.locator("#settings-page")).toBeHidden();
  await expect(page.locator(".shell-session-title")).toHaveText("UI");

  // 8. Send one message and see the answer on screen, from the controlled server.
  await page.locator("#chat-task").fill("journey: say something");
  await page.locator("#chat-send").click();
  await expect(page.locator("#chat-log")).toContainText("ui harness reply", { timeout: 60000 });
  await expect(page.locator(".chat-list-name.working")).toHaveCount(0);

  // 2ry W0: replay the reported sequence in the disposable harness before the
  // held-response case below: switch, answer, Settings > Chats, choose a face.
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="chats"]').click();
  await page.locator('[data-menu-path="chat.typeface"]').click();
  await page.locator('button[data-action="config-choice"][data-path="chat.typeface"][data-value="Arial"]').click();
  await expect.poll(async () => (await (await page.request.get(`${harness.base}/api/state`)).json()).config.chat.typeface).toBe("Arial");
  await expect(page.locator("[data-save-status]")).not.toContainText("Saving changes");
  await toggleSettings(page);

  // 9. Delete the chat through its four-entry row menu.
  const selected = page.locator(".chat-list-row.selected");
  const session = await selected.getAttribute("data-session");
  await selected.hover();
  await selected.locator(".chat-list-more").click();
  await selected.locator(".chat-list-row-menu").getByRole("button", { name: "Delete", exact: true }).click();
  await selected.locator(".chat-list-delete-confirm").getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.locator(`.chat-list-row[data-session="${session}"]`)).toHaveCount(0);

  // 10. Remove the connection, through the anchored confirmation (item 2l4), and
  // watch the row go.
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${id}"]`).click();
  const popover = page.locator(".confirm-popover");
  await expect(popover).toBeVisible();
  await popover.locator('[data-action="confirm-proceed"]').click();

  await expect(page.locator(`.connection-row[data-id="${id}"] .connection-summary`)).toHaveCount(0);
  await expect(page.locator(".model-row")).toHaveCount(rowsBefore);
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
	await page.getByText("Add signed-in credential").click();
	for (const selector of ["#credential-entra-name", "#credential-entra-origin", "#credential-tenant", "#credential-client-id", "#credential-scopes"]) {
		await expect(page.locator(selector)).toHaveValue("");
	}
	const evidence = join(repo, "candidate", "rel-1.44.0-w7-entra", "settings-credentials.png");
	await mkdir(join(repo, "candidate", "rel-1.44.0-w7-entra"), { recursive: true });
	await page.screenshot({ path: evidence, fullPage: true });
	await page.close();
});

test("Google credentials expose browser sign-in without device code 2xh", async () => {
	const page = await harness.context.newPage();
	await page.route("**/api/credentials", async (route) => route.fulfill({
		status: 200, contentType: "application/json",
		body: JSON.stringify({ credentials: [{ name: "google-mail", kind: "google", origin: "https://gmail.googleapis.com:443", stored_at: "2026-11-02T00:00:00Z", account: "someone@example.org", sign_in_needed: false }] }),
	}));
	await page.goto(`${harness.base}/chat`);
	await toggleSettings(page); await page.locator('.settings-nav [data-id="shell"]').click();
	await expect(page.getByText("signed in as someone@example.org")).toBeVisible();
	await expect(page.locator('[data-action="credential-sign-in"][data-id="google-mail"]')).toHaveText("Switch account");
	await expect(page.locator('[data-action="credential-device-code"][data-id="google-mail"]')).toHaveCount(0);
	await page.getByText("Add signed-in credential").click();
	await page.locator("#credential-provider").selectOption("google");
	for (const selector of ["#credential-google-name", "#credential-google-origin", "#credential-google-client-id", "#credential-google-client-secret", "#credential-google-scopes"]) await expect(page.locator(selector)).toBeVisible();
	const evidence = join(repo, "logs", "evidence", "rel-1.62.191", "captures"); await mkdir(evidence, { recursive: true });
	for (const width of [1024, 1440]) {
		await page.setViewportSize({ width, height: 900 });
		await page.locator("#credential-google-scopes").scrollIntoViewIfNeeded();
		expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".settings-content"))).toEqual([]);
		await page.screenshot({ path: join(evidence, `google-credentials-${width}.png`), fullPage: true });
	}
	await page.close();
});

test("adding an already covered directory says which parent allows it 2vj", async () => {
	const page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat`);
	const token = await page.locator('meta[name="agentb-mutation-token"]').getAttribute("content");
	const parent = "C:\\fixture\\parent";
	const long = "C:\\fixture\\a-very-long-directory-name-that-must-end-in-an-ellipsis-at-the-cell-edge";
	const seeded = await page.request.post(`${harness.base}/api/config`, { headers: { "X-AgentB-Mutation-Token": token }, data: { shell: { trusted_folders: [{ path: parent, source: "settings" }, { path: long, source: "settings" }, { path: "not-a-path", source: "settings" }] } } });
	expect(seeded.ok(), await seeded.text()).toBe(true);
	await toggleSettings(page); await page.locator('.settings-nav [data-id="shell"]').click();
	const evidence = join(repo, "logs", "evidence", "rel-1.62.127", "captures"); await mkdir(evidence, { recursive: true });
	for (const width of [1280, 1024]) {
		await page.setViewportSize({ width, height: 860 });
		expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".settings-content"))).toEqual([]);
		const style = await page.locator(".allowed-directory-path").nth(1).evaluate((node) => { const css = getComputedStyle(node); return { ellipsis: css.textOverflow, clipped: node.scrollWidth > node.clientWidth, title: node.title, background: css.backgroundColor }; });
		expect(style).toEqual({ ellipsis: "ellipsis", clipped: true, title: long, background: "rgba(0, 0, 0, 0)" });
		await page.screenshot({ path: join(evidence, `after-${width}.png`), fullPage: true });
		await page.locator(".allowed-directory-path").evaluateAll((nodes) => nodes.forEach((node) => { const input = document.createElement("input"); input.value = node.textContent; input.id = node.id; node.replaceWith(input); }));
		await page.screenshot({ path: join(evidence, `before-${width}.png`), fullPage: true });
		await page.locator('[id^="trusted-folder-"]:not(#trusted-folder-new)').evaluateAll((nodes) => nodes.forEach((node) => { const span = document.createElement("span"); span.textContent = node.value; span.id = node.id; span.className = "allowed-directory-path"; span.contentEditable = "plaintext-only"; span.setAttribute("role", "textbox"); span.setAttribute("aria-label", "Allowed directory"); span.title = node.value; node.replaceWith(span); }));
	}
	await page.locator("#trusted-folder-new").fill(`${parent}\\child`); await page.locator('[data-action="trusted-folder-add"]').click();
	await expect(page.locator(".settings-feedback", { hasText: `already allowed by ${parent}` })).toBeVisible();
	await expect(page.locator(".allowed-directory-path")).toHaveCount(3);
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

test("the ready screen telemetry switch is shared with About and persists both choices", async () => {
	test.setTimeout(120_000);
	const evidence = join(repo, "candidate", "rel-1.45.0-w2-telemetry");
	await mkdir(evidence, { recursive: true });
	const page = await harness.context.newPage();
	for (const enabled of [false, true]) {
		await page.goto(`${harness.base}/setup`);
		await page.getByRole("button", { name: /More options/ }).click(); await page.getByLabel("Address").fill(`http://127.0.0.1:${harness.modelPort}`); await page.getByLabel("Model").fill("journey-model"); await page.getByRole("button", { name: "Connect" }).click(); await page.getByRole("button", { name: "Next" }).click();
		await expect(page.locator("h1")).toHaveText("Agent_b is ready");
		const consent = page.getByRole("switch");
		if ((await consent.getAttribute("aria-checked")) !== String(enabled)) await consent.click();
		if (!enabled) await page.screenshot({ path: join(evidence, "setup-anonymous-data.png"), fullPage: true });
		await page.getByRole("button", { name: "Finish" }).click(); await page.waitForURL(/\/chat/);
		expect((await harness.getState()).config.telemetry.enabled).toBe(enabled);
	}
	await page.goto(`${harness.base}/chat`);
	await expect(page).toHaveURL(/\/chat/);
	await expect(page.locator("#setup")).toHaveCount(0);
	await toggleSettings(page);
	await page.locator('.settings-nav [data-id="about"]').click();
	await expect(page.getByText("anonymous diagnostics", { exact: true })).toBeVisible();
	await page.screenshot({ path: join(evidence, "settings-about-anonymous-data.png"), fullPage: true });
	await page.close();
});

test("service identity remains under Settings Experimental at three widths 2tf", async () => {
	test.setTimeout(120_000);
	const evidence = join(repo, "logs", "evidence", "rel-1.62.75", "captures");
	await mkdir(evidence, { recursive: true });
	const sizes = [{ width: 304, height: 720 }, { width: 1280, height: 860 }, { width: 1920, height: 1080 }];
	const capture = async (page) => {
		for (const size of sizes) {
			await page.setViewportSize(size);
			const layout = await page.locator(".settings-content").evaluate((section) => {
				const box = section.getBoundingClientRect();
				const controls = [...section.querySelectorAll("button")].map((node) => node.getBoundingClientRect());
				return { pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth, inside: box.left >= -1 && box.right <= innerWidth + 1 && controls.every((item) => item.left >= box.left - 1 && item.right <= box.right + 1), textWhole: [...section.querySelectorAll("h1,p")].every((node) => node.scrollWidth <= node.clientWidth + 1) };
			});
			expect(layout, `service identity ${size.width}`).toEqual({ pageOverflow: 0, inside: true, textWhole: true });
			await page.screenshot({ path: join(evidence, `service-identity-settings-${size.width}.png`), fullPage: true });
		}
	};
	const page = await harness.context.newPage(); await page.goto(`${harness.base}/chat`); await toggleSettings(page); await page.locator('.settings-nav [data-id="shell"]').click(); await expect(page.locator(".settings-content")).toContainText("Experimental"); await expect(page.getByRole("switch", { name: "Service identity" })).toBeVisible(); await capture(page); await page.close();
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
  for (let index = 0; index < 10; index++) {
    const before = await page.locator(".model-row").count();
    await page.locator('[data-action="add-connection"]').click();
    await expect(page.locator(".model-row")).toHaveCount(before + 1);
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
  const editor = page.locator(".model-editor");
  const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];
  posts.length = 0;

  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed by hand");
  await editor.locator(`[data-path="connections.${id}.label"]`).evaluate((node) => node.blur());
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
  const editor = page.locator(".model-editor");
  const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];
  await page.locator('.server-editor [data-server-field="base_url"]').fill("192.0.2.1:8080");

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

// 2qx replaces the two identity halves with the settings people change;
// the action line and collapsed Advanced section remain beneath them.
test("the probe verdict is a dated fact and Test names what it sent and got 2wb", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await page.locator('.connection-defaults > summary').click();
  await expect(page.locator(".connection-capabilities li").filter({ hasText: "probe: rejected" })).toHaveText(/^probe: rejected · \d{4}-\d{2}-\d{2}$/);
  await page.screenshot({ path: join(repo, "test-results", "2wb-verdict.png") });
  await page.locator('[data-action="connection-toggle"][data-id="ui"]').click();
  await expect(page.locator(".connection-editor")).toHaveCount(0);
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".model-editor");
  const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];
  const address = page.locator('.server-editor [data-server-field="base_url"]');
  const addressSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await address.fill(`http://127.0.0.1:${harness.modelPort}`);
  await address.evaluate((node) => node.blur());
  await addressSaved;
  const modelsListed = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${id}/models`));
  await editor.locator(`[data-path="connections.${id}.model"]`).click();
  await modelsListed;
  let model = page.locator(`[data-path="connections.${id}.model"]`);
  if (await model.evaluate((node) => node.tagName === "SELECT")) {
    await model.selectOption("__type__");
    model = page.locator(`[data-path="connections.${id}.model"]`);
  }
  await model.fill("second-model");
  const modelSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await model.press("Enter");
  await modelSaved;
  const tested = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${id}/probe`));
  await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).click();
  await tested;
  await expect(page.locator(".connection-editor .discovery-note")).toContainText(/Test passed in \d+ ms · images: accepts images but does not read them \(sent digit \d; got "ui harness reply"\)/);
  await expect(page.locator(".connection-capabilities li").filter({ hasText: "probe:" }), "an unprobed connection has no verdict").toHaveCount(0);
  await page.screenshot({ path: join(repo, "test-results", "2wb-connections.png") });
  await page.close();
});

test("the model sheet keeps its five ordered fields above its actions and Advanced", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".model-editor");
  const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];

	const labels = async () => (await editor.locator(":scope > .connection-fields > .connection-primary > .setting-row > label").allTextContents()).filter(Boolean);
	expect(await labels()).toEqual(["name", "agent profile", "model", "context size", "reads images"]);
  await expect(page.locator(".connection-defaults")).not.toHaveAttribute("open", /.*/);
  await expect(page.locator(".connection-defaults > summary")).toHaveText("Advanced");
  await expect(editor.locator(".connection-primary-actions button")).toHaveText(["Test", "Eval", "Recommended"]);

	// The empty new connection does not collapse or reorder the fields.
	await expect(page.locator(`.connection-editor [data-action="measure-connection"][data-id="${id}"]`)).toBeVisible();
	expect(await labels()).toEqual(["name", "agent profile", "model", "context size", "reads images"]);
  await page.close();
});

test("servers group their models and add one without repeating server fields 2y4", async () => {
  test.setTimeout(120000);
  const evidence = join(repo, "logs", "evidence", "rel-1.62.206", "captures"); await mkdir(evidence, { recursive: true });
  const page = await harness.context.newPage();
  const network = [];
  page.on("request", (request) => { if (/\/models$|\/probe$|\/chat\/completions$/.test(new URL(request.url()).pathname)) network.push(request.url()); });
  await page.goto(`${harness.base}/chat`);
  await page.evaluate(async () => {
    const bus = await import("/static/js/bus.js");
    await bus.api("/api/connections/ui/duplicate", { id: "ui-second", label: "Second model" });
    await bus.api("/api/config", { connections: [{ id: "ui-second", model: "second-model" }, { id: "third-server", label: "Third server", base_url: "http://192.0.2.20:8080", model: "third-model" }] });
    bus.reduce({ type: "snapshot", data: await bus.api("/api/state", undefined, "GET") });
  });
  await toggleSettings(page);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await expect(page.locator(".server-row")).toHaveCount(2);
  await expect(page.locator(".model-row")).toHaveCount(3);
  await expect(page.locator(".server-row").first()).toContainText("2 models");
  for (const [name, width, height] of [["narrow", 304, 700], ["medium", 1280, 800], ["wide", 1920, 1080]]) { await page.setViewportSize({ width, height }); expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".settings-content"))).toEqual([]); await page.screenshot({ path: join(evidence, `2y4-list-${name}.png`) }); }
  await page.locator('.model-row[data-id="ui-second"]').click();
  const editor = page.locator(".model-editor");
  await expect(editor.locator('[data-path$=".base_url"], [data-path$=".api_key"], [data-path$=".protocol"], [data-path$=".request_timeout_s"]')).toHaveCount(0);
  for (const [name, width, height] of [["narrow", 304, 700], ["medium", 1280, 800], ["wide", 1920, 1080]]) { await page.setViewportSize({ width, height }); expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".settings-content"))).toEqual([]); await page.screenshot({ path: join(evidence, `2y4-model-open-${name}.png`) }); }
  await page.locator('.model-row[data-id="ui-second"]').click();
  await page.locator(".server-row").first().click();
  const server = page.locator(".server-editor");
  await expect(server.locator("[data-server-field]")).toHaveCount(4);
  for (const [name, width, height] of [["narrow", 304, 700], ["medium", 1280, 800], ["wide", 1920, 1080]]) { await page.setViewportSize({ width, height }); expect(auditLayoutSnapshot(await page.evaluate(collectLayoutSnapshot, ".settings-content"))).toEqual([]); await page.screenshot({ path: join(evidence, `2y4-server-open-${name}.png`) }); }
  const address = server.locator('[data-server-field="base_url"]');
  const original = await address.inputValue();
  await address.fill("http://127.0.0.1:19090"); await address.press("Enter");
  await expect.poll(async () => (await harness.getState()).config.connections.filter((item) => ["ui", "ui-second"].includes(item.id)).map((item) => item.base_url)).toEqual(["http://127.0.0.1:19090", "http://127.0.0.1:19090"]);
  await expect(page.locator("[data-save-status]")).toContainText("All changes saved");
  network.length = 0;
  await page.locator('.connection-add-model[data-id="ui"]').evaluate((node) => node.click());
  await expect.poll(() => page.evaluate(() => document.activeElement?.dataset?.path || "")).toMatch(/^connections\.[^.]+\.model$/);
  const addedID = (await page.evaluate(() => document.activeElement.dataset.path)).split(".")[1];
  const added = (await harness.getState()).config.connections.find((item) => item.id === addedID);
  expect(added).toMatchObject({ base_url: "http://127.0.0.1:19090", model: "" });
  expect(network, "Add model contacted a model server").toEqual([]);
  await page.evaluate(async ({ ids, original }) => {
    const bus = await import("/static/js/bus.js");
    await bus.api("/api/config", { connections: [{ id: "ui", base_url: original }] });
    for (const id of ids) await bus.api(`/api/connections/${id}`, undefined, "DELETE");
  }, { ids: [addedID, "ui-second", "third-server"], original });
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
	const editor = page.locator(".model-editor");
	const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];
	const address = page.locator('.server-editor [data-server-field="base_url"]');
	const addressSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
	await address.fill(`127.0.0.1:${harness.modelPort}`);
	await address.evaluate((node) => node.blur());
	await addressSaved;
	const modelsListed = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${id}/models`));
	await editor.locator(`[data-path="connections.${id}.model"]`).click();
	await modelsListed;

	let model = page.locator(`[data-path="connections.${id}.model"]`);
	if (await model.evaluate((node) => node.tagName === "SELECT")) {
		await model.selectOption("__type__");
		model = page.locator(`[data-path="connections.${id}.model"]`);
	}
	await model.fill("second-model");
  await expect(model, "the field forgot the pick as soon as it was made").toHaveValue("second-model");
  const modelSaved = page.waitForResponse((response) => response.url().endsWith("/api/config") && response.request().method() === "POST");
  await model.evaluate((node) => node.blur());
  await modelSaved;

  // Test again: the field still shows what he picked, not what is saved.
  const tested = page.waitForResponse((response) => response.url().endsWith(`/api/connections/${id}/probe`));
  await page.locator(`.connection-editor [data-action="probe"][data-id="${id}"]`).click({ force: true });
  await tested;
  await expect(model, "Test drew the saved model over his pick").toHaveValue("second-model");

  // Save wrote it, and the field still shows it afterwards.
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
  await page.locator('[data-menu-path="chat.typeface"]').click();
  await page.locator(`button[data-action="config-choice"][data-path="chat.typeface"][data-value="${draft}"]`).click();
  await expect(page.locator("[data-save-status]")).toContainText("Saving changes");
  await expect(page.locator("[data-save-status]")).toContainText("no answer", { timeout: 11000 });
  await expect(page.locator('[data-menu-path="chat.typeface"]')).toContainText(draft);
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
  const editor = page.locator(".model-editor");
  const id = (await editor.locator("[data-path$='.model']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed while refused");
  await page.locator('.server-editor [data-server-field="base_url"]').fill("127.0.0.1:9");

  // The literal placeholder is not a model, so the server refuses to write it back —
  // which is exactly what kept it alive on the operator's disk.
  await page.route("**/api/config", (route) => route.fulfill({ status: 400, contentType: "application/json", body: JSON.stringify({ error: "model is empty", field: `connections.${id}.model` }) }));
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
  await expect(page.locator('.server-editor [data-server-field="base_url"]')).toHaveValue("127.0.0.1:9");
  await page.close();
});

test("finished turns undo their own real file changes before and after restart 2vk", async () => {
	test.setTimeout(120000);
	const original = (name) => `${name}-original\n`;
	let page = await harness.context.newPage();
	let initialState = await harness.getState();
	let sessionID = Object.keys(initialState.sessions)[0];
	if (!sessionID) {
		const created = await page.request.post(`${harness.base}/api/sessions`, { data: { label: "Undo acceptance" }, headers: { "X-AgentB-Mutation-Token": harness.mutationToken } });
		expect(created.status()).toBe(201);
		await expect.poll(async () => Object.keys((await harness.getState()).sessions).length).toBe(1);
		initialState = await harness.getState();
		sessionID = Object.keys(initialState.sessions)[0];
	}
	// Item 2wp: away to another profile and back, as 2sb does earlier in this file; the
	// undo records written after that must still be held after the restart below.
	const headers = { "X-AgentB-Mutation-Token": harness.mutationToken }, active = initialState.profiles?.active;
	await page.request.post(`${harness.base}/api/profiles`, { headers, data: { action: "create", name: "Undo fixture" } });
	for (const name of ["Undo fixture", active]) expect((await page.request.post(`${harness.base}/api/profiles`, { headers, data: { action: "switch", name } })).ok()).toBe(true);
	const workspace = initialState.sessions[sessionID].workspace;
	for (const name of ["alpha", "beta", "gamma"]) for (const suffix of ["one", "two"]) await writeFile(join(workspace, `undo-${name}-${suffix}.txt`), original(`${name}-${suffix}`));
	await page.goto(`${harness.base}/chat?session=${sessionID}`);
	const run = async (name) => {
		await page.locator("#chat-task").fill(`acceptance: undo-files ${name}`);
		await page.locator("#chat-send").click();
		// The loaded full suite crossed the former 30 s response bound; the
		// controlled response completed on the same tree when run alone.
		await expect(page.locator(".chat-response").filter({ hasText: `UNDO FILES ${name} DONE` })).toBeVisible({ timeout: 60000 });
		await expect.poll(async () => (await harness.getState()).sessions[sessionID].run.status).toBe("idle");
	};
	await run("alpha");
	const alpha = page.locator(".chat-response").filter({ hasText: "UNDO FILES alpha DONE" });
	await alpha.getByRole("button", { name: "2 files changed" }).click();
	await expect(alpha.locator(".chat-file-open")).toHaveCount(2);
	for (const file of await alpha.locator(".chat-file-open").all()) await file.click();
	await expect(alpha.locator(".chat-file-diff")).toHaveCount(2);
	await alpha.locator(".chat-file-undo").first().click();
	await page.locator(".confirm-popover").getByRole("button", { name: "Undo", exact: true }).click();
	await expect(alpha.getByRole("button", { name: /1 files? changed/ })).toBeVisible();
	await alpha.getByRole("button", { name: "Undo file changes" }).click();
	await page.locator(".confirm-popover").getByRole("button", { name: "Undo", exact: true }).click();
	await expect(alpha.locator(".chat-work-undo-result")).toContainText("restored undo-alpha-two.txt");
	for (const suffix of ["one", "two"]) expect(await readFile(join(workspace, `undo-alpha-${suffix}.txt`), "utf8")).toBe(original(`alpha-${suffix}`));

	await run("beta");
	await run("gamma");
	await page.close();
	await harness.stop();
	harness = await start({ exe: join(root, "Agent_b.exe"), appRoot: repo, data: join(root, "data"), modelIDs: ["journey-model", "second-model"], streamDelayMs: 800, reuseConfig: true });
	page = await harness.context.newPage();
	await page.goto(`${harness.base}/chat?session=${sessionID}`);
	const replay = (await harness.getState()).sessions[sessionID].chat;
	const runIDs = Object.fromEntries(["beta", "gamma"].map((name) => [name, replay.find((entry) => entry.text === `UNDO FILES ${name} DONE`)?.run_id]));
	expect(runIDs.beta).toBeTruthy();
	expect(runIDs.gamma).toBeTruthy();
	expect(runIDs.beta).not.toBe(runIDs.gamma);
	const beta = page.locator(".chat-response").filter({ hasText: "UNDO FILES beta DONE" });
	const gamma = page.locator(".chat-response").filter({ hasText: "UNDO FILES gamma DONE" });
	await beta.getByRole("button", { name: "2 files changed" }).click();
	await expect(beta.locator(".chat-file-open")).toHaveCount(2);
	for (const file of await beta.locator(".chat-file-open").all()) await file.click();
	await expect(beta.locator(".chat-file-diff")).toHaveCount(2);
	const betaUndo = page.waitForRequest((request) => request.url().endsWith("/api/file-undo") && request.method() === "POST");
	await beta.getByRole("button", { name: "Undo file changes" }).click();
	await page.locator(".confirm-popover").getByRole("button", { name: "Undo", exact: true }).click();
	expect((await betaUndo).postDataJSON().run_id).toBe(runIDs.beta);
	await expect(beta.locator(".chat-work-undo-result")).toContainText("restored undo-beta-one.txt");
	for (const suffix of ["one", "two"]) {
		expect(await readFile(join(workspace, `undo-beta-${suffix}.txt`), "utf8")).toBe(original(`beta-${suffix}`));
		expect(await readFile(join(workspace, `undo-gamma-${suffix}.txt`), "utf8")).toBe(`gamma-${suffix}-changed\n`);
	}
	await gamma.getByRole("button", { name: "Undo file changes" }).click();
	await page.locator(".confirm-popover").getByRole("button", { name: "Undo", exact: true }).click();
	await expect(gamma.locator(".chat-work-undo-result")).toContainText("restored undo-gamma-one.txt");
	for (const suffix of ["one", "two"]) expect(await readFile(join(workspace, `undo-gamma-${suffix}.txt`), "utf8")).toBe(original(`gamma-${suffix}`));
	await page.close();
});
