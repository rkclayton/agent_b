import { mkdir, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");

const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");
const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

test("2qm tight edges themed scrollbars stacked paperclip and four-pixel handle", async ({ page }) => {
  await page.setViewportSize({ width: 1400, height: 800 });
  await page.setContent(`<style>${tokensCSS}\n${chatCSS}</style>
    <div class="chat-page">
      <header class="app-shell"></header><div class="chat-budget"></div>
      <main id="chat-log" class="chat-log"><section class="chat-entry"><div class="chat-speaker">agent</div><div class="chat-content">line</div></section></main>
      <footer id="chat-composer" class="chat-composer">
        <div id="chat-resize-handle" class="chat-resize-handle"></div>
        <div class="chat-composer-row"><div class="chat-input-wrap"><textarea></textarea><span class="chat-input-actions">
          <span class="chat-attach-wrap"><button id="chat-attach" class="chat-attach composer-control">attach</button></span>
          <button id="chat-mic" class="chat-mic composer-control">mic</button><button class="composer-control">send</button>
        </span></div></div>
      </footer>
    </div>`);
  const measured = await page.evaluate(() => {
    const log = document.querySelector("#chat-log"), row = document.querySelector(".chat-entry"), composer = document.querySelector("#chat-composer");
    const textarea = document.querySelector("textarea"), attach = document.querySelector("#chat-attach"), mic = document.querySelector("#chat-mic"), handle = document.querySelector("#chat-resize-handle");
    const rowBox = row.getBoundingClientRect(), attachBox = attach.getBoundingClientRect(), micBox = mic.getBoundingClientRect();
    const rowStyle = getComputedStyle(row), composerStyle = getComputedStyle(composer);
    const scrollbar = getComputedStyle(log, "::-webkit-scrollbar"), track = getComputedStyle(log, "::-webkit-scrollbar-track"), thumb = getComputedStyle(log, "::-webkit-scrollbar-thumb");
    return {
      today: { rowHorizontal: 12, rowVertical: 8, composerSide: 8 },
      rowPadding: [parseFloat(rowStyle.paddingTop), parseFloat(rowStyle.paddingRight), parseFloat(rowStyle.paddingBottom), parseFloat(rowStyle.paddingLeft)],
      composerSide: [parseFloat(composerStyle.paddingLeft), parseFloat(composerStyle.paddingRight)],
      rowEdgeGaps: [rowBox.left, innerWidth - rowBox.right],
      scrollbar: { width: scrollbar.width, track: track.backgroundColor, thumb: thumb.backgroundColor, radius: thumb.borderRadius, standard: getComputedStyle(log).scrollbarColor },
      composerScrollbarWidth: getComputedStyle(textarea, "::-webkit-scrollbar").width,
      controls: { attach: [attachBox.width, attachBox.height], mic: [micBox.width, micBox.height], x: Math.abs((attachBox.left + attachBox.right - micBox.left - micBox.right) / 2), gap: micBox.top - attachBox.bottom },
      handle: { height: handle.getBoundingClientRect().height, cursor: getComputedStyle(handle).cursor }
    };
  });
  expect(measured.rowPadding[1]).toBeLessThanOrEqual(measured.today.rowHorizontal * .75);
  expect(measured.rowPadding[3]).toBeLessThanOrEqual(measured.today.rowHorizontal * .75);
  expect(measured.rowPadding[0]).toBeLessThanOrEqual(measured.today.rowVertical * .75);
  expect(measured.rowPadding[2]).toBeLessThanOrEqual(measured.today.rowVertical * .75);
  expect(Math.max(...measured.composerSide)).toBeLessThanOrEqual(measured.today.composerSide * .75);
  expect(Math.max(...measured.rowEdgeGaps)).toBeLessThanOrEqual(4);
  expect(measured.scrollbar).toEqual({ width: "6px", track: "rgb(13, 16, 20)", thumb: "rgb(112, 125, 139)", radius: "0px", standard: "rgb(112, 125, 139) rgb(13, 16, 20)" });
  expect(measured.composerScrollbarWidth).toBe("6px");
  expect(measured.controls.attach).toEqual(measured.controls.mic);
  expect(measured.controls.x).toBeLessThanOrEqual(.5);
  expect(measured.controls.gap).toBeGreaterThanOrEqual(0);
  expect(measured.controls.gap).toBeLessThanOrEqual(4);
  expect(measured.handle).toEqual({ height: 4, cursor: "row-resize" });
});

test("the etched current exists only for an active run and is removed for reduced motion", async ({ page }) => {
  await page.setContent(`<style>${tokensCSS}</style><div class="etch-current"></div>`);
  const current = page.locator(".etch-current");
  await expect(current).toHaveCSS("opacity", "0");
  await page.locator("body").evaluate((body) => body.classList.add("run-active"));
  await expect(current).toHaveCSS("opacity", "0.05");
  await expect(current).not.toHaveCSS("animation-name", "none");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await expect(current).toHaveCSS("display", "none");
});

test("an installer refusal replaces restarting and offers Install again 2t6", async ({ browser }) => {
  const session = { schema_version: 1, complete: true, id: "chat-1", label: "Update proof", agent_id: "agent_b", role: "b", connection_id: "fixture", b_connection: "fixture", created_at: "2026-10-09T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] }, timeline: [], chat: [], runnable: true, closed: false };
  const update = { enabled: true, available: true, version: "v9.9.9", installing: false };
  const snapshot = { sessions: { [session.id]: session }, connections: [{ id: "fixture", label: "fixture" }], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [{ id: "fixture", label: "fixture" }] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {}, update };
  const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
  await context.addInitScript(({ data }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: "chat-1", surface: { kind: "chat", key: "chat-1" } }));
    class FixtureEvents { constructor() { this.listeners = new Map(); globalThis.fixtureEvents = this; setTimeout(() => { const event = { data: JSON.stringify({ type: "snapshot", data }) }; for (const listener of this.listeners.get("snapshot") || []) listener(event); }); } addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); } emit(type, payload) { const event = { data: JSON.stringify({ type, data: payload }) }; for (const listener of this.listeners.get(type) || []) listener(event); } close() {} }
    globalThis.EventSource = FixtureEvents; globalThis.chrome = { webview: {} };
  }, { data: snapshot });
  const page = await context.newPage();
  await page.route("**/*", async (route) => { const request = route.request(), url = new URL(request.url()); if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML }); if (url.pathname === "/api/update" && request.method() === "POST") return route.fulfill({ status: 202, contentType: "application/json", body: JSON.stringify({ started: true, update: { ...update, installing: true } }) }); if (url.pathname === "/api/chats/tree") return route.fulfill({ contentType: "application/json", body: JSON.stringify({ folders: [], chats: [{ id: "chat-1", folder: "" }] }) }); if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" }); return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") }); });
  await page.goto("http://localhost:59999/chat?setup=skip&session=chat-1", { waitUntil: "domcontentloaded" });
  await page.locator("#chat-update-install").click();
  await expect(page.locator("#chat-notice")).toContainText("Update verified; restarting into this chat…");
  await expect(page.locator("#chat-status-line")).toBeVisible();
  await expect(page.locator("#chat-update-install")).toHaveText("Starting…");
  await page.locator("#chat-status-line").evaluate((node) => node.scrollIntoView({ block: "end" }));
  await mkdir("test-results/2t6-update", { recursive: true });
  await page.screenshot({ path: "test-results/2t6-update/installing.png" });
  const refusal = 'installer signature verification failed: payload signer "CN=Agent_b Disposable Test Signing" is not the pinned AgentB release key';
  await page.evaluate((reason) => globalThis.fixtureEvents.emit("update.changed", { enabled: true, available: true, version: "v9.9.9", installing: false, error: reason }), refusal);
  await expect(page.locator("#chat-notice")).toContainText(refusal);
  await expect(page.locator("#chat-notice")).toHaveClass(/alarm/);
  await expect(page.locator("#chat-status-line")).toBeVisible();
  await expect(page.locator("#chat-update-install")).toHaveText("Install");
  await page.locator("#chat-status-line").evaluate((node) => node.scrollIntoView({ block: "end" }));
  await page.screenshot({ path: "test-results/2t6-update/refused.png" });
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.evaluate(() => globalThis.fixtureEvents.emit("update.changed", { enabled: true, available: false, current_version: "v9.9.9", outcome: { ok: true, version: "v9.9.9" } }));
  await expect(page).toHaveURL(/session=chat-1/);
  await page.screenshot({ path: "test-results/2t6-update/returned.png" });
  await context.close();
});
