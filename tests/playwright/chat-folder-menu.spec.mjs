import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

// Item 2qb CHECKS 1-4: every folder head has the icon controls, add preserves
// its parent, nested folders render nested, and both rename paths work.
test("folder-plus nests and pencils rename folders and chats", async ({ browser }) => {
  const id = "chat-0";
  const session = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 1 }, complete: true, id, label: "Chat name",
    agent_id: "agent_b", role: "b", created_at: "2026-10-04T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {},
    activity: { completed_stages: [] }, timeline: [], chat: [], runnable: true, closed: false };
  const snapshot = () => ({ sessions: { [id]: session }, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] },
    flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} });
  let tree = { folders: ["A"], chats: [{ id, folder: "A" }] };
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
      }
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(tree) });
    }
    if (url.pathname === `/api/sessions/${id}` && request.method() === "POST") session.label = request.postDataJSON().label;
    if (url.pathname === "/api/state") return route.fulfill({ contentType: "application/json", body: JSON.stringify(snapshot()) });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
  });
  await page.goto(`http://localhost:59999/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
  await page.locator(".agent-tab-wrap.selected .agent-tab").click({ button: "right" });
  const menu = page.locator(".agent-tab-wrap.selected .agent-chat-menu");
  await expect(menu.getByTitle("New folder in chats")).toBeVisible();
  await expect(menu.getByTitle("New folder in A")).toBeVisible();
  expect(await menu.locator("button").evaluateAll((items) => items.map((item) => item.textContent.trim()).filter((text) => text === "New folder" || text === "Rename"))).toEqual([]);

  page.once("dialog", (dialog) => dialog.accept("B"));
  await menu.getByTitle("New folder in A").click();
	await expect(menu.locator('[data-folder="A"] > [data-folder="A/B"]')).toHaveCount(1);
	await menu.locator('[data-folder="A"] > summary').click();
  await expect(menu.locator('[data-folder="A"] > [data-folder="A/B"]')).toBeVisible();
  expect(actions.at(-1)).toEqual({ action: "add", parent: "A", name: "B" });

  page.once("dialog", (dialog) => dialog.accept("Z"));
  await menu.getByTitle("Rename A").click();
  await expect(menu.locator('[data-folder="Z"]')).toBeVisible();
	await menu.locator('[data-folder="Z"] > summary').click();

  await menu.getByTitle("Rename Chat name").click();
  await menu.getByLabel("Chat name").fill("Renamed chat");
  await menu.getByTitle("Save chat name").click();
	await menu.locator('[data-folder="Z"] > summary').click();
  await expect(menu.getByTitle("Rename Renamed chat")).toBeVisible();
  await page.screenshot({ path: "test-results/2qb-nested-chat-menu.png" });
  await context.close();
});
