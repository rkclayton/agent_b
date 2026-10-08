import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");
const evidence = process.env.AGENTB_EVIDENCE_DIR || "test-results/2qz-evidence";
const robotEvidence = process.env.AGENTB_2S2_EVIDENCE_DIR || "test-results/2s2-evidence";

test("chat list rows, actions, independent state, resize persistence and captures 2qz", async ({ browser }) => {
  test.setTimeout(60000);
  await mkdir(evidence, { recursive: true });
  const base = { schema_version: 1, complete: true, agent_id: "agent_b", role: "b", connection_id: "fixture", b_connection: "fixture", tools: [], messages: [], budget: {}, activity: { completed_stages: [] }, timeline: [], chat: [], runnable: true, closed: false };
  const sessions = {
    p1: { ...base, id: "p1", label: "Pinned older", created_at: "2026-10-01T00:00:00Z", last_activity: "2026-10-06T04:00:00Z", run: { status: "running" } },
    p2: { ...base, id: "p2", label: "Pinned newest", role: "d", created_at: "2026-10-02T00:00:00Z", last_activity: "2026-10-06T05:00:00Z", run: { status: "running" } },
    f1: { ...base, id: "f1", label: "Folder running", created_at: "2026-10-03T00:00:00Z", last_activity: "2026-10-06T03:00:00Z", run: { status: "running" } },
    f2: { ...base, id: "f2", label: "Folder closed", created_at: "2026-10-04T00:00:00Z", last_activity: "2026-10-06T02:00:00Z", closed: true, run: { status: "idle" } },
    root: { ...base, id: "root", label: "A chat name long enough to end in an ellipsis while remaining whole on hover", created_at: "2026-10-05T00:00:00Z", last_activity: "2026-10-06T02:30:00Z", run: { status: "idle" } },
    worker: { ...base, id: "worker", label: "Worker", role: "c", created_at: "2026-10-06T00:00:00Z", last_activity: "2026-10-06T06:00:00Z", run: { status: "running" } },
  };
  let tree = { folders: ["Work"], chats: [
    { id: "p1", folder: "Work", pinned: true }, { id: "p2", folder: "", pinned: true },
    { id: "f1", folder: "Work", pinned: false }, { id: "f2", folder: "Work", pinned: false },
    { id: "root", folder: "", pinned: false }, { id: "worker", folder: "", pinned: false },
  ], archived: [{ id: "old", name: "Archived chat" }] };
  const actions = [];
  const snapshot = () => ({ sessions, connections: [{ id: "fixture", label: "fixture" }], config: { agents: [{ name: "agent_b", b: "fixture", d: "fixture" }], connections: [{ id: "fixture", label: "fixture" }] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} });
  const context = await browser.newContext({ viewport: { width: 1400, height: 800 } });
  await context.addInitScript(({ data }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "p1", surface: { kind: "chat", key: "p1" } }));
    class FixtureEvents { constructor() { this.listeners = new Map(); setTimeout(() => { this.onopen?.(); const event = { data: JSON.stringify({ type: "snapshot", data }) }; for (const listener of this.listeners.get("snapshot") || []) listener(event); }); } addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); } close() {} }
    globalThis.EventSource = FixtureEvents;
  }, { data: snapshot() });
  const page = await context.newPage();
  await page.route("**/*", async (route) => {
    const request = route.request(), url = new URL(request.url());
    if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
    if (url.pathname === "/api/state") return route.fulfill({ contentType: "application/json", body: JSON.stringify(snapshot()) });
    if (url.pathname === "/api/chats/tree") {
      if (request.method() === "POST") {
        const body = request.postDataJSON(); actions.push(body);
        if (body.action === "pin") tree.chats.find((chat) => chat.id === body.id).pinned = body.pinned;
        if (body.action === "move") tree.chats.find((chat) => chat.id === body.id).folder = body.folder;
      }
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(tree) });
    }
    const match = url.pathname.match(/^\/api\/sessions\/([^/]+)(?:\/(reopen))?$/);
    if (match && match[2] === "reopen") { actions.push({ action: "reopen", id: match[1] }); sessions[match[1]].closed = false; return route.fulfill({ contentType: "application/json", body: "{}" }); }
    if (match && request.method() === "POST") { sessions[match[1]].label = request.postDataJSON().label; return route.fulfill({ contentType: "application/json", body: JSON.stringify({ session: sessions[match[1]] }) }); }
    if (match && request.method() === "DELETE") { delete sessions[match[1]]; tree.chats = tree.chats.filter((chat) => chat.id !== match[1]); return route.fulfill({ contentType: "application/json", body: "{}" }); }
    if (url.pathname === "/api/sessions" && request.method() === "POST") { const id = `new${Object.keys(sessions).length}`; sessions[id] = { ...base, id, label: "New chat", created_at: "2026-10-06T07:00:00Z", run: { status: "idle" } }; tree.chats.push({ id, folder: "", pinned: false }); return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ session: sessions[id] }) }); }
    if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
  });
  await page.goto("http://localhost:59999/chat?setup=skip&session=p1", { waitUntil: "domcontentloaded" });
  const rows = page.locator(".chat-list-row[data-session]");
  await expect(rows).toHaveCount(5);
  expect(await rows.evaluateAll((items) => items.map((item) => item.dataset.session))).toEqual(["p2", "p1", "f1", "f2", "root"]);
  await expect(page.locator('[data-session="worker"]')).toHaveCount(0);
  await expect(page.locator('.chat-list-row[data-session="p1"] .chat-list-state')).toHaveClass(/running/);
  await expect(page.locator('.chat-list-row[data-session="p2"] .chat-list-state')).toHaveClass(/running/);
  await expect(page.locator('.chat-list-row[data-session="f1"] .chat-list-state')).toHaveClass(/running/);
	const headings = page.locator(".chat-list-group-name, .chat-list-folder > summary, .chat-list-archived > summary");
	for (const size of [9, 12, 32]) {
		await page.evaluate((value) => document.documentElement.style.setProperty("--chat-scale", String(value / 12)), size);
		const heights = await headings.evaluateAll((nodes) => nodes.map((node) => node.getBoundingClientRect().height));
		const rowHeight = await rows.first().evaluate((node) => node.getBoundingClientRect().height);
		expect(heights.every((height) => Math.abs(height - rowHeight) < 0.1), `${size}px: headings=${heights} row=${rowHeight}`).toBe(true);
	}
	await page.evaluate(() => document.documentElement.style.removeProperty("--chat-scale"));
	const folderHead = page.locator('.chat-list-folder[data-folder="Work"] > summary'), folderTools = folderHead.locator("button");
	await expect(folderTools).toHaveCount(3); for (const tool of await folderTools.all()) await expect(tool).toBeHidden();
	const nameX = await folderHead.locator(".chat-list-folder-name").evaluate((node) => node.getBoundingClientRect().x);
	await folderHead.hover(); for (const tool of await folderTools.all()) await expect(tool).toBeVisible();
	expect(await folderHead.locator(".chat-list-folder-name").evaluate((node) => node.getBoundingClientRect().x)).toBe(nameX);
	await page.mouse.move(1000, 400); await folderHead.focus(); for (const tool of await folderTools.all()) await expect(tool).toBeVisible();
	await expect(page.locator(".chat-list-group.root .chat-list-folder-action")).toHaveCount(2);
	for (const action of await page.locator(".chat-list-folder-action, .chat-list-folder-delete").all()) expect(await action.evaluate((node) => getComputedStyle(node).backgroundColor)).toBe("rgba(0, 0, 0, 0)");
  const robot = page.locator(".shell-app-robot"), setStates = async (values, selected = "p1") => page.evaluate(async ({ values, selected }) => { const bus = await import("/static/js/bus.js"); for (const session of Object.values(bus.store.sessions)) Object.assign(session, { pending_approval: false, pending_repo_policy: false, model_unreachable: false, run: { status: "idle" } }); for (const [id, value] of Object.entries(values)) Object.assign(bus.store.sessions[id], value); bus.setSelection("agent_b", selected); bus.reduce({ type: "config.changed", data: { config: bus.store.config } }); }, { values, selected });
  for (const step of [
    [{}, "p1", "idle", "idle"],
    [{ f1: { run: { status: "running" } } }, "p1", "running", "1 running"],
    [{ f1: { run: { status: "running" } }, p2: { run: { status: "paused" }, pending_approval: true } }, "p1", "waiting", "1 waiting for you · 1 running"],
    [{ f1: { run: { status: "running" } } }, "p1", "running", "1 running"],
    [{}, "p1", "idle", "idle"],
    [{ p1: { model_unreachable: true } }, "p1", "offline", "model unreachable"],
    [{ f1: { run: { status: "running" } } }, "", "running", "1 running"],
  ]) { await setStates(step[0], step[1]); await expect(robot).toHaveClass(new RegExp(step[2])); await expect(robot).toHaveAttribute("title", step[3]); await expect(robot).toHaveAttribute("aria-label", step[3]); expect(await robot.innerText()).toBe(""); }
  expect(await robot.evaluate((node) => node.tabIndex)).toBe(-1);
  const selectedBeforeRobotClick = await page.evaluate(async () => (await import("/static/js/bus.js")).store.selection.session_id);
  await robot.click();
  expect(await page.evaluate(async () => (await import("/static/js/bus.js")).store.selection.session_id)).toBe(selectedBeforeRobotClick);
  await page.emulateMedia({ reducedMotion: "reduce" });
  for (const state of ["idle", "running", "waiting", "offline"]) { await robot.evaluate((node, value) => { node.className = `shell-app-robot chat-run-robot ${value}`; }, state); expect(await robot.evaluate((node) => ({ animation: getComputedStyle(node).animationDuration, color: getComputedStyle(node).color }))).toMatchObject({ animation: "0s", color: state === "running" ? "rgb(60, 240, 138)" : state === "idle" ? "rgb(112, 125, 139)" : "rgb(228, 98, 79)" }); }
  await page.emulateMedia({ reducedMotion: "no-preference" }); await setStates({ f1: { run: { status: "running" } } }); await page.mouse.click(1000, 400); await mkdir(robotEvidence, { recursive: true });
  for (const width of [304, 1280, 1920]) { await page.setViewportSize({ width, height: 800 }); const layout = await page.evaluate(() => { const shell = document.querySelector("#app-shell").getBoundingClientRect(), head = document.querySelector(".shell-app-robot").getBoundingClientRect(), right = document.querySelector(".shell-right").getBoundingClientRect(); return { height: shell.height, first: head.left, size: head.height, font: parseFloat(getComputedStyle(document.querySelector(".shell-session-title")).fontSize), inside: head.right <= right.left && right.right <= innerWidth && [...document.querySelectorAll(".shell-right > :not(.shell-menu)")].every((node) => { const box = node.getBoundingClientRect(); return box.width === 0 || box.right <= innerWidth; }) }; }); expect(layout).toEqual({ height: 32, first: 4, size: 12, font: 12, inside: true }); await page.screenshot({ path: join(robotEvidence, `${width}-chat.png`) }); await page.locator(".shell-settings").click(); await page.screenshot({ path: join(robotEvidence, `${width}-settings.png`) }); await page.locator(".shell-settings").click(); }
  const menuCases = async (opener, menu, other, otherMenu) => {
    const openControl = async () => { const control = page.locator(opener); if (opener.includes("chat-list-more")) await control.locator("xpath=..").hover(); await control.click(); }; await openControl();
    const blocked = await page.locator(menu).evaluate((open) => [...document.querySelectorAll("button,input,textarea,select,summary")].filter((control) => {
      const box = control.getBoundingClientRect(), menuBox = open.getBoundingClientRect(), x = box.left + box.width / 2, y = box.top + box.height / 2;
      const style = getComputedStyle(control);
      return control.checkVisibility({ checkOpacity: true, checkVisibilityCSS: true }) && !control.disabled && style.display !== "none" && style.visibility !== "hidden" && x >= 0 && y >= 0 && x <= innerWidth && y <= innerHeight && !open.contains(control) && !(x >= menuBox.left && x <= menuBox.right && y >= menuBox.top && y <= menuBox.bottom) && !control.contains(document.elementFromPoint(x, y));
    }).map((control) => control.id || control.className || control.tagName));
    expect(blocked, `${menu} blocked outside control centres: ${blocked.join(", ")}`).toEqual([]);
    await page.mouse.click(1000, 400); await expect(page.locator(menu)).toBeHidden();
    await openControl(); await page.locator("#chat-task").click(); await expect(page.locator(menu)).toBeHidden(); await expect(page.locator("#chat-task")).toBeFocused();
    await openControl(); await page.keyboard.press("Escape"); await expect(page.locator(menu)).toBeHidden();
    await openControl(); await page.locator(other).click(); await expect(page.locator(menu)).toBeHidden(); await expect(page.locator(otherMenu)).toBeVisible(); await page.keyboard.press("Escape");
  };
  for (const row of [
    [".chat-list-new", ".shell-new-menu", "#chat-attach", ".chat-attach-menu"],
    [".shell-session-title", ".shell-connection-menu", "#chat-attach", ".chat-attach-menu"],
    ['.chat-list-row[data-session="root"] .chat-list-more', '.chat-list-row[data-session="root"] .chat-list-row-menu', "#chat-attach", ".chat-attach-menu"],
    ["#chat-attach", ".chat-attach-menu", ".shell-session-title", ".shell-connection-menu"],
  ]) await menuCases(...row);
  await page.locator('.chat-list-row[data-session="root"]').hover(); await page.locator('.chat-list-row[data-session="root"] .chat-list-more').click();
  await page.evaluate(async () => { const bus = await import("/static/js/bus.js"); bus.reduce({ type: "snapshot", data: { ...bus.store, build: {} } }); });
  await expect(page.locator('.chat-list-row[data-session="root"] .chat-list-row-menu')).toBeVisible();
  await page.keyboard.press("Escape");
  await page.screenshot({ path: join(evidence, "2qz-panel-default.png") });
  await page.screenshot({ path: join(evidence, "2qz-three-running.png") });
  await page.locator('.chat-list-folder[data-folder="Work"] > summary').click();
  await page.screenshot({ path: join(evidence, "2qz-folder-open.png") });
  await page.screenshot({ path: join(evidence, "2qz-long-name.png") });

  await page.locator('.chat-list-row[data-session="f2"] .chat-list-name').click();
  await expect(page.locator('.chat-list-row[data-session="f2"]')).toHaveClass(/selected/);
  expect(actions).toContainEqual({ action: "reopen", id: "f2" });
  expect(actions.some((action) => action.action === "close" || action.action === "stop")).toBe(false);

  await page.locator('.chat-list-row[data-session="root"]').hover(); await page.locator('.chat-list-row[data-session="root"] .chat-list-more').click();
  const menu = page.locator('.chat-list-row[data-session="root"] .chat-list-row-menu');
  await expect(menu.locator(":scope > button")).toHaveText(["Pin", "Rename", "Move to folder", "Delete"]);
  await page.screenshot({ path: join(evidence, "2qz-row-menu.png") });
  await page.keyboard.press("Escape");
  await expect(menu).toBeHidden();
  await page.locator('.chat-list-row[data-session="root"]').hover(); await page.locator('.chat-list-row[data-session="root"] .chat-list-more').click();
  await menu.getByRole("button", { name: "Pin", exact: true }).click();
  expect(actions.at(-1)).toEqual({ action: "pin", id: "root", pinned: true });
  await page.locator('.chat-list-row[data-session="root"]').hover(); await page.locator('.chat-list-row[data-session="root"] .chat-list-more').click();
  await menu.getByRole("button", { name: "Move to folder", exact: true }).click();
  await menu.getByRole("button", { name: "Work", exact: true }).click();
  expect(actions.at(-1)).toEqual({ action: "move", id: "root", folder: "Work" });

  tree.folders.push("External");
  await page.evaluate(async () => {
    const bus = await import("/static/js/bus.js");
    bus.reduce({ type: "chat.list.patch", data: { operation: "add", path: "External" } });
  });
  await expect(page.locator('.chat-list-folder[data-folder="External"]')).toBeVisible();

  await page.locator(".chat-list-new").click();
  await expect(page.locator(".shell-new-menu")).toBeVisible();
  await page.getByTitle("Open chat").click();
  await expect(rows).toHaveCount(6);

  const edge = await page.locator(".chat-list-resize").boundingBox();
  await page.mouse.move(edge.x + 2, edge.y + 20); await page.mouse.down(); await page.mouse.move(0, edge.y + 20); await page.mouse.up();
  await expect(page.locator(".chat-list-handle")).toBeVisible();
  await page.screenshot({ path: join(evidence, "2qz-panel-hidden.png") });
  await page.reload({ waitUntil: "domcontentloaded" });
  await expect(page.locator(".chat-list-handle")).toBeVisible();
  await page.mouse.move(2, 100); await page.mouse.down(); await page.mouse.move(100, 100); await page.mouse.up();
  await expect(page.locator(".chat-list-panel")).toBeVisible();
  await page.setViewportSize({ width: 304, height: 254 });
  await page.screenshot({ path: join(evidence, "2qz-panel-narrowest.png") });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1)).toBe(true);
  const text = await page.locator(".chat-list-panel").evaluate((panel) => { const out = [], walker = document.createTreeWalker(panel, NodeFilter.SHOW_TEXT); for (let node = walker.nextNode(); node; node = walker.nextNode()) if (node.textContent.trim()) out.push(node.textContent.trim()); return out; });
  expect(text.some((value) => /[.!?](?:\s|$)/.test(value))).toBe(false);
  await context.close();
});

test("removing tabs leaves every top-right control at its prior 1400px coordinates 2qz", async ({ browser }) => {
  const oldShell = execFileSync("git", ["show", "HEAD:web/js/shell.js"], { encoding: "utf8" });
  const currentShell = await readFile(new URL("../../web/js/shell.js", import.meta.url), "utf8");
  const session = { schema_version: 1, complete: true, id: "chat-0", label: "Chat", agent_id: "agent_b", role: "b", connection_id: "fixture", b_connection: "fixture", created_at: "2026-10-06T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] }, timeline: [], chat: [], runnable: true, closed: false };
  const snapshot = { sessions: { [session.id]: session }, connections: [{ id: "fixture", label: "fixture" }], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [{ id: "fixture", label: "fixture" }] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} };
  const measure = async (shell) => {
    const context = await browser.newContext({ viewport: { width: 1400, height: 800 } });
    await context.addInitScript(({ data }) => { sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-0", surface: { kind: "chat", key: "chat-0" } })); class FixtureEvents { constructor() { this.listeners = new Map(); setTimeout(() => { const event = { data: JSON.stringify({ type: "snapshot", data }) }; for (const listener of this.listeners.get("snapshot") || []) listener(event); }); } addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); } close() {} } globalThis.EventSource = FixtureEvents; }, { data: snapshot });
    const page = await context.newPage();
    await page.route("**/*", async (route) => { const url = new URL(route.request().url()); if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML }); if (url.pathname === "/static/js/shell.js") return route.fulfill({ contentType: "text/javascript", body: shell }); if (url.pathname === "/api/chats/tree") return route.fulfill({ contentType: "application/json", body: JSON.stringify({ folders: [], chats: [{ id: "chat-0", folder: "" }] }) }); if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" }); return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") }); });
    await page.goto("http://localhost:59999/chat?setup=skip&session=chat-0", { waitUntil: "domcontentloaded" });
    await page.evaluate(() => document.fonts.ready);
    await expect(page.locator(".shell-right")).toBeVisible();
    await page.locator(".shell-session-title").evaluate((node) => { node.style.width = ""; });
    const boxes = await page.locator(".shell-right > :not(.shell-menu)").evaluateAll((nodes) => nodes.map((node) => { const box = node.getBoundingClientRect(); return [node.className, box.x, box.y, box.width, box.height]; }));
    await context.close();
    return boxes;
  };
  expect(await measure(currentShell)).toEqual(await measure(oldShell));
});
