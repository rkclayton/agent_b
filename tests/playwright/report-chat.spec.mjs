import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

// Item 2qa CHECK 1: the old manual report action is absent from the chat menu.
test("the chat menu has no report action", async ({ browser }) => {
  const id = "chat-0";
  const snapshot = {
    sessions: { [id]: { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 1 }, complete: true, id, label: "strange chat",
      role: "b", created_at: "2026-10-04T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] },
      timeline: [], chat: [], runnable: true, closed: false } },
    connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [],
    profiles: { active: "", names: [] }, build: {},
  };
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
  }, { snapshot });
  const page = await context.newPage();
  const reports = [];
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
    if (url.pathname === `/api/sessions/${id}/report`) reports.push(route.request().method());
    if (url.pathname.startsWith("/api/")) return route.fulfill({ status: 404, contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
  });
  await page.goto(`http://localhost:59999/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
  await page.locator(".chat-list-row[data-session='chat-0'] .chat-list-more").click();
  await expect(page.getByRole("button", { name: "Report this chat" })).toHaveCount(0);
  await page.screenshot({ path: "test-results/2qa-chat-menu-no-report.png" });
  expect(reports).toEqual([]);
  await context.close();
});
