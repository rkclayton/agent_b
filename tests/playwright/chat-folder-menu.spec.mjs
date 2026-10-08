import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");
const renameEvidence = process.env.AGENTB_2SN_EVIDENCE_DIR || "test-results/2sn-evidence";

// Item 2qb CHECKS 1-4: every folder head has the icon controls, add preserves
// its parent, nested folders render nested, and both rename paths work.
test("folder-plus nests and pencils rename folders and chats", async ({ browser }) => {
  const id = "chat-0";
  const session = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 1 }, complete: true, id, label: "Chat name",
    agent_id: "agent_b", role: "b", created_at: "2026-10-04T00:00:00Z", run: { status: "running" }, tools: [], messages: [], budget: {},
    activity: { completed_stages: [] }, timeline: [], chat: [], runnable: true, closed: false };
	const other = { ...session, id: "chat-1", label: "Other chat", cursor: { generation: "chat-1.jsonl", offset: 1 } }; let archived = false, refuseRename = false;
  const snapshot = () => ({ sessions: archived ? { [other.id]: other } : { [id]: session, [other.id]: other }, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] },
    flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} });
	let tree = { folders: ["A"], chats: [{ id, folder: "A" }, { id: other.id, folder: "" }], archived: [] };
  const actions = [];
  const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
  await context.addInitScript(({ snapshot }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-0", surface: { kind: "chat", key: "chat-0" } }));
    class FixtureEvents {
      constructor() {
        this.listeners = new Map();
        setTimeout(() => {
          this.onopen?.();
          const event = { data: JSON.stringify({ type: "snapshot", data: snapshot }) };
          for (const listener of this.listeners.get("snapshot") || []) listener(event);
        });
      }
      addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
      close() {}
    }
    globalThis.EventSource = FixtureEvents;
  }, { snapshot: snapshot() });
  const page = await context.newPage();
  await page.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
    if (url.pathname === "/api/chats/tree") {
      if (request.method() === "POST") {
        const body = request.postDataJSON(); actions.push(body);
        if (body.action === "add") tree = { ...tree, folders: [...tree.folders, `${body.parent}/${body.name}`] };
        if (body.action === "rename") tree = { folders: [body.name, `${body.name}/B`], chats: tree.chats.map((chat) => ({ ...chat, folder: body.name })) };
		if (body.action === "archive") { archived = true; tree = { ...tree, chats: tree.chats.filter((chat) => chat.id !== id), archived: [{ id, name: session.label, folder: "Z" }] }; }
		if (body.action === "restore") { archived = false; tree = { ...tree, chats: [...tree.chats, { id, folder: "Z" }], archived: [] }; }
      }
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(tree) });
    }
    if (url.pathname === `/api/sessions/${id}` && request.method() === "POST") {
      if (refuseRename) { refuseRename = false; return route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: "session not found", field: "session" }) }); }
      session.label = request.postDataJSON().label;
    }
    if (url.pathname === "/api/state") return route.fulfill({ contentType: "application/json", body: JSON.stringify(snapshot()) });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
  });
  await page.goto(`http://localhost:59999/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
  const panel = page.locator(".chat-list-panel");
  await expect(panel).toBeVisible();
  await expect(panel.getByTitle("New folder", { exact: true })).toHaveCount(0);
  await panel.locator(".chat-list-menu-button").click(); await expect(page.locator(".chat-list-main-menu").getByRole("button", { name: "Folder", exact: true })).toBeVisible(); await page.locator("#chat-log").click();
  await expect(panel.getByTitle("New folder in A")).toBeHidden();

  page.once("dialog", (dialog) => dialog.accept("B"));
	await panel.locator('[data-folder="A"] > summary').focus(); await page.keyboard.press("Tab");
	await expect(panel.getByTitle("New folder in A")).toBeFocused(); await page.keyboard.press("Enter");
	await expect(panel.locator('[data-folder="A"] > [data-folder="A/B"]')).toHaveCount(1);
	await panel.locator('[data-folder="A"] > summary').click();
  await expect(panel.locator('[data-folder="A"] > [data-folder="A/B"]')).toBeVisible();
  expect(actions.at(-1)).toEqual({ action: "add", parent: "A", name: "B" });

  page.once("dialog", (dialog) => dialog.accept("Z"));
	await panel.locator('[data-folder="A"] > summary').hover();
  await panel.getByTitle("Rename A").click();
  await expect(panel.locator('[data-folder="Z"]')).toBeVisible();
	await panel.locator('[data-folder="Z"] > summary').click();

  const chatRow = panel.locator(`[data-session="${id}"]`);
  await mkdir(renameEvidence, { recursive: true });
  for (const width of [240, 96]) {
    await page.evaluate((value) => document.documentElement.style.setProperty("--chat-list-width", `${value}px`), width);
    for (const size of [9, 12, 32]) {
      await page.evaluate((value) => document.documentElement.style.setProperty("--chat-scale", String(value / 12)), size);
      const nameBox = await chatRow.locator(".chat-list-name").boundingBox(), nextY = (await panel.locator(`[data-session="${other.id}"]`).boundingBox()).y;
      await chatRow.hover(); await chatRow.locator(".chat-list-more").click(); await panel.getByRole("button", { name: "Rename", exact: true }).click();
      const inputBox = await panel.getByLabel("Chat name").boundingBox();
      await expect(panel.getByLabel("Chat name")).not.toHaveClass(/working/);
      expect(Math.abs(inputBox.x - nameBox.x)).toBeLessThanOrEqual(1); expect(Math.abs(inputBox.width - nameBox.width)).toBeLessThanOrEqual(1);
      expect(Math.abs((await panel.locator(`[data-session="${other.id}"]`).boundingBox()).y - nextY)).toBeLessThanOrEqual(1);
      await page.screenshot({ path: join(renameEvidence, `${width}-${size}-rename.png`) });
      await page.keyboard.press("Escape");
    }
  }
  await page.evaluate(() => { document.documentElement.style.setProperty("--chat-list-width", "240px"); document.documentElement.style.removeProperty("--chat-scale"); });
  await chatRow.hover();
  await chatRow.locator(".chat-list-more").click();
  await panel.getByRole("button", { name: "Rename", exact: true }).click();
  const editor = panel.getByLabel("Chat name"), beforeRow = await chatRow.boundingBox();
  await expect(editor).toBeFocused();
  await expect(editor).toHaveValue("Chat name");
  await expect(panel.getByTitle("Save chat name")).toHaveCount(0);
  await editor.fill("Renamed chat");
  await page.keyboard.press("Enter");
  await expect(editor).toHaveCount(0);
  await expect(chatRow.locator(".chat-list-name")).toHaveText("Renamed chat");
  await expect(chatRow.locator(".chat-list-name")).toHaveClass(/working/);
  const afterRow = await chatRow.boundingBox();
  expect(afterRow.height).toBe(beforeRow.height);

  await chatRow.hover(); await chatRow.locator(".chat-list-more").click(); await panel.getByRole("button", { name: "Rename", exact: true }).click();
  await panel.getByLabel("Chat name").fill("Cancelled"); await page.keyboard.press("Escape");
  await expect(chatRow.locator(".chat-list-name")).toHaveText("Renamed chat");

  await chatRow.hover(); await chatRow.locator(".chat-list-more").click(); await panel.getByRole("button", { name: "Rename", exact: true }).click();
  await panel.getByLabel("Chat name").fill("Redraw-safe");
  await panel.getByLabel("Chat name").evaluate((node) => node.setSelectionRange(3, 7));
  await page.evaluate(async () => { const bus = await import("/static/js/bus.js"); for (let index = 0; index < 20; index++) bus.reduce({ type: "config.changed", data: { config: bus.store.config } }); });
  await expect(panel.getByLabel("Chat name")).toBeFocused(); await expect(panel.getByLabel("Chat name")).toHaveValue("Redraw-safe");
  expect(await panel.getByLabel("Chat name").evaluate((node) => [node.selectionStart, node.selectionEnd])).toEqual([3, 7]);
  await page.locator("#chat-log").click(); await expect(chatRow.locator(".chat-list-name")).toHaveText("Redraw-safe");

  await chatRow.hover(); await chatRow.locator(".chat-list-more").click(); await panel.getByRole("button", { name: "Rename", exact: true }).click();
  await panel.getByLabel("Chat name").fill("   "); await page.keyboard.press("Enter"); await expect(chatRow.locator(".chat-list-name")).toHaveText("Redraw-safe");

  refuseRename = true;
  await chatRow.hover(); await chatRow.locator(".chat-list-more").click(); await panel.getByRole("button", { name: "Rename", exact: true }).click();
  await panel.getByLabel("Chat name").fill("Refused"); await page.keyboard.press("Enter");
  await expect(chatRow.locator(".chat-list-name")).toHaveText("Redraw-safe"); await expect(page.locator(".shell-session-title")).toHaveClass(/alarm/);
  tree = { ...tree, chats: tree.chats.filter((chat) => chat.id !== id), archived: [{ id, name: "Renamed chat", folder: "Z" }] };
  await page.reload({ waitUntil: "domcontentloaded" });
	await expect(page.getByText("Archived", { exact: true })).toBeVisible(); await page.getByText("Archived", { exact: true }).click(); await expect(page.getByTitle("Restore Renamed chat")).toBeVisible();
	await page.screenshot({ path: "test-results/2qi-archived-chat-menu.png" }); await page.getByTitle("Restore Renamed chat").click();
  await context.close();
});

test("scrolling up loads a 3,000-entry chat fifty at a time to its first entry 2qc", async ({ browser }) => {
	const id = "chat-0";
	const row = (index) => ({ type: "user", key: `m${index}`, text: `entry ${index}` });
	const session = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 1 }, complete: true, id, label: "Long chat",
		agent_id: "agent_b", role: "b", created_at: "2026-10-05T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {},
		activity: { completed_stages: [] }, timeline: [], chat: Array.from({ length: 50 }, (_, index) => row(2950 + index)),
		history_start: 2950, history_end: 3000, history_total: 3000, runnable: true, closed: false };
	const snapshot = () => ({ sessions: { [id]: session }, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] },
		flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} });
	const context = await browser.newContext({ viewport: { width: 900, height: 420 } });
	await context.addInitScript(({ snapshot }) => {
		sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-0", surface: { kind: "chat", key: "chat-0" } }));
		class FixtureEvents {
			constructor() { this.listeners = new Map(); setTimeout(() => { this.onopen?.(); const event = { data: JSON.stringify({ type: "snapshot", data: snapshot }) }; for (const listener of this.listeners.get("snapshot") || []) listener(event); }); }
			addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
		}
		globalThis.EventSource = FixtureEvents;
	}, { snapshot: snapshot() });
	let historyCalls = 0;
	const page = await context.newPage();
	await page.route("**/*", async (route) => {
		const url = new URL(route.request().url());
		if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
		if (url.pathname === `/api/sessions/${id}/history`) {
			const before = Number(url.searchParams.get("before") || 3000), start = Math.max(0, before - 50);
			historyCalls++;
			return route.fulfill({ contentType: "application/json", body: JSON.stringify({ session_id: id, start, before, total: 3000, chat: Array.from({ length: before - start }, (_, index) => row(start + index)) }) });
		}
		if (url.pathname === "/api/state") return route.fulfill({ contentType: "application/json", body: JSON.stringify(snapshot()) });
		if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
		return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
	});
	await page.goto(`http://localhost:59999/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
	await expect(page.locator('[data-entry-key="m2950"]')).toBeVisible();
	while (historyCalls < 59) {
		const before = historyCalls;
		await page.locator("#chat-log").evaluate((node) => { node.scrollTop = 0; node.dispatchEvent(new Event("scroll")); });
		await expect.poll(() => historyCalls).toBeGreaterThan(before);
	}
	expect(historyCalls).toBe(59);
	await expect(page.locator('[data-entry-key="m0"]')).toBeVisible();
	expect(await page.locator(".chat-entry").count()).toBeLessThanOrEqual(350);
	await context.close();
});

test("pointer resting on a tool row does not detach newest-line follow 2qf", async ({ browser }) => {
	const id = "chat-follow";
	const tool = { type: "tool", key: "tool:t0", callID: "t0", name: "shell", args: { command: "Write-Output ready" }, result: { ok: true, preview: "ready" }, content: "ready" };
	const session = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 1 }, complete: true, id, label: "Follow",
		agent_id: "agent_b", role: "b", created_at: "2026-10-05T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {},
		activity: { completed_stages: [] }, timeline: [], chat: [tool], runnable: true, closed: false };
	const snapshot = { sessions: { [id]: session }, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] },
		flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} };
	const context = await browser.newContext({ viewport: { width: 900, height: 420 } });
	await context.addInitScript(({ snapshot }) => {
		sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-follow", surface: { kind: "chat", key: "chat-follow" } }));
		class FixtureEvents {
			constructor() { this.listeners = new Map(); globalThis.followEvents = this; setTimeout(() => this.emit("snapshot", { type: "snapshot", data: snapshot })); }
			addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
			emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); }
			close() {}
		}
		globalThis.EventSource = FixtureEvents;
	}, { snapshot });
	const page = await context.newPage();
	await page.route("**/*", async (route) => {
		const url = new URL(route.request().url());
		if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
		if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
		return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
	});
	await page.goto(`http://localhost:59999/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
	await page.locator('[data-entry-key="tool:t0"] button').first().hover();
	await page.evaluate(({ id }) => {
		for (let index = 1; index <= 20; index++) {
			globalThis.followEvents.emit("projection.patch", { type: "projection.patch", data: { schema_version: 1, session_id: id,
				previous_cursor: { generation: `${id}.jsonl`, offset: index }, cursor: { generation: `${id}.jsonl`, offset: index + 1 },
				operations: [{ op: "append", path: "/chat", value: { type: "user", key: `new-${index}`, text: `new entry ${index}` } }] } });
		}
	}, { id });
	await expect(page.locator('[data-entry-key="new-20"]')).toBeVisible();
	const atLatest = await page.locator("#chat-log").evaluate((node) => node.scrollHeight - node.clientHeight - node.scrollTop <= 24);
	expect(atLatest).toBe(true);
	await context.close();
});
