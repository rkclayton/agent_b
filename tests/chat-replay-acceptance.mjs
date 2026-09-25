import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { createServer } from "node:net";
import { dirname, join } from "node:path";
import { chromium } from "@playwright/test";

const args = Object.fromEntries(Array.from({ length: Math.floor(process.argv.slice(2).length / 2) }, (_, index) => {
  const offset = index * 2 + 2;
  return [process.argv[offset].replace(/^--/, ""), process.argv[offset + 1]];
}));
for (const key of ["app", "data", "replay", "evidence"]) assert.ok(args[key], `missing --${key}`);
await mkdir(dirname(args.evidence), { recursive: true });
await mkdir(args.evidence, { recursive: true });
const configPath = join(args.data, "harness.json");
const config = JSON.parse(await readFile(configPath, "utf8"));
const replayConnection = config.connections?.[0]?.id || "local";
config.agents = [{
  name: "Acceptance",
  b: replayConnection,
  c: replayConnection,
  d: replayConnection,
  toolset: ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service", "delegate"],
}];
const portProbe = createServer();
await new Promise((resolve) => portProbe.listen(0, "127.0.0.1", resolve));
const port = portProbe.address().port;
await new Promise((resolve) => portProbe.close(resolve));
config.listen = `127.0.0.1:${port}`;
await writeFile(configPath, `${JSON.stringify(config, null, 2)}\n`);
const tape = await readFile(args.replay, "utf8");
const recordCount = tape.split(/\r?\n/).filter(Boolean).length;
let sessionID = args.session || "main";
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const json = async (url, headers = {}) => {
  const response = await fetch(url, { headers });
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`${response.status} ${JSON.stringify(value)}`);
  return value;
};
const waitHTTP = async (url, timeout = Math.max(30000, recordCount * 25), headers = {}) => {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try { return await json(url, headers); } catch { await sleep(50); }
  }
  throw new Error(`timed out waiting for ${url}`);
};
const bootstrapBrowserSession = async (origin) => {
  const page = await fetch(`${origin}/chat`);
  if (!page.ok) throw new Error(`browser bootstrap page returned ${page.status}`);
  const html = await page.text();
  const token = html.match(/<meta name="agentb-mutation-token" content="([^"]+)">/)?.[1];
  assert.ok(token, "browser bootstrap token missing");
  const response = await fetch(`${origin}/api/browser-session`, { method: "POST", headers: { "X-AgentB-Mutation-Token": token } });
  assert.equal(response.status, 204, `browser bootstrap returned ${response.status}`);
  const cookie = response.headers.get("set-cookie")?.split(";", 1)[0];
  assert.ok(cookie, "browser session cookie missing");
  return cookie;
};

const children = [];
let browser;
try {
  const app = spawn(join(args.app, "Agent_b.exe"), ["-config", join(args.data, "harness.json"), "-app-root", args.app, "-data-root", args.data, "-replay", args.replay], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"] });
  children.push(app);
  app.stdout.on("data", (chunk) => process.stdout.write(chunk));
  app.stderr.on("data", (chunk) => process.stderr.write(chunk));
  const origin = `http://127.0.0.1:${port}`;
  await waitHTTP(`${origin}/api/connections`, Math.max(30000, recordCount * 25));
  const browserCookie = await bootstrapBrowserSession(origin);
  const finalState = await waitHTTP(`${origin}/api/state`, Math.max(30000, recordCount * 25), { Cookie: browserCookie });
  if (!finalState.sessions?.[sessionID] && !args.session) sessionID = Object.keys(finalState.sessions || {})[0];
  const finalSession = finalState.sessions?.[sessionID];
  assert.ok(finalSession, `session ${sessionID} missing from replay`);

  browser = await chromium.launch({ channel: "msedge", headless: true });
  const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
  const [cookieName, cookieValue] = browserCookie.split("=", 2);
  await context.addCookies([{ name: cookieName, value: cookieValue, url: origin }]);
  await context.addInitScript(() => {
    const evidence = window.__agentbStreamingReplay = { renderErrors: [], remounts: [], toolKeys: [], patchEvents: 0, stateFetches: [] };
    const NativeEventSource = window.EventSource;
    window.EventSource = class extends NativeEventSource {
      constructor(...values) {
        super(...values);
        this.addEventListener("projection.patch", () => { evidence.patchEvents++; });
      }
    };
    const nativeFetch = window.fetch.bind(window);
    window.fetch = (...values) => {
      if (String(values[0]).includes("/api/state")) evidence.stateFetches.push(new Error("state fetch").stack);
      return nativeFetch(...values);
    };
    const originalError = console.error.bind(console);
    console.error = (...values) => {
      const line = values.map((value) => {
        try { return typeof value === "string" ? value : JSON.stringify(value); }
        catch { return String(value); }
      }).join(" ");
      if (line.includes("chat render failure")) evidence.renderErrors.push(line);
      originalError(...values);
    };
    const rows = new Map();
    const inspect = (root) => {
      if (!(root instanceof Element)) return;
      const candidates = root.matches("[data-entry-key]") ? [root] : [];
      candidates.push(...root.querySelectorAll("[data-entry-key]"));
      for (const row of candidates) {
        const tool = row.querySelector(".tool-tick");
        const failure = row.classList.contains("chat-render-failure");
        if (!tool && !failure) continue;
        const key = row.dataset.entryKey;
        if (!key) continue;
        const previous = rows.get(key);
        if (previous && previous !== row) evidence.remounts.push({ key, from: previous.className, to: row.className });
        rows.set(key, row);
        if (tool && !evidence.toolKeys.includes(key)) evidence.toolKeys.push(key);
      }
    };
    new MutationObserver((mutations) => {
      for (const mutation of mutations) for (const node of mutation.addedNodes) inspect(node);
    }).observe(document, { childList: true, subtree: true });
  });
  const page = await context.newPage();
  const pageErrors = [];
  const consoleErrors = [];
  const failedResponses = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  page.on("console", (message) => { if (message.type() === "error") consoleErrors.push(message.text()); });
  page.on("response", (response) => { if (response.status() >= 400) failedResponses.push({ status: response.status(), url: response.url() }); });
  const replayTimeout = Math.max(30000, recordCount * 25);
  page.setDefaultTimeout(replayTimeout);
  await page.goto(`http://127.0.0.1:${port}/chat?session=${encodeURIComponent(sessionID)}`, { waitUntil: "domcontentloaded" });
  const finalCursor = finalSession.cursor;
  const replayDeadline = Date.now() + replayTimeout;
  let streamedCursor;
  while (Date.now() < replayDeadline) {
    streamedCursor = await page.evaluate(async (id) => (await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href)).store.sessions?.[id]?.cursor, sessionID);
    if (streamedCursor?.generation === finalCursor.generation && Number(streamedCursor?.offset || 0) === Number(finalCursor.offset || 0)) break;
    await sleep(100);
  }
  assert.deepEqual(streamedCursor, finalCursor, `streaming replay did not reach the final cursor in ${replayTimeout} ms`);
  await page.waitForTimeout(150);
  const result = await page.evaluate(async (id) => {
    const { store } = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    return {
      cursor: store.sessions?.[id]?.cursor,
      mountedRenderFailures: document.querySelectorAll(".chat-render-failure").length,
      ...window.__agentbStreamingReplay,
    };
  }, sessionID);
  assert.deepEqual(result.cursor, finalCursor, JSON.stringify(result));
  assert.equal(result.patchEvents, recordCount, `streaming replay frame count differs from tape records: ${JSON.stringify(result)}`);
  assert.deepEqual(result.stateFetches.filter((stack) => stack.includes("at resync")), [], JSON.stringify(result.stateFetches));
  assert.ok(result.toolKeys.length > 0, `streaming replay did not mount any condensed tool rows: ${JSON.stringify(result)}`);
  assert.deepEqual(result.remounts, [], JSON.stringify(result.remounts));
  assert.deepEqual(result.renderErrors, [], JSON.stringify(result.renderErrors));
  assert.equal(result.mountedRenderFailures, 0, JSON.stringify(result));
  const ui = await page.evaluate(() => {
    const prose = [...document.querySelectorAll(".chat-response-prose")];
    const folds = [...document.querySelectorAll(".chat-step-summary")];
    const decided = [...document.querySelectorAll(".approval-decided")];
    const alarmSummaries = [
      ...document.querySelectorAll(".chat-response.alarm > .chat-response-content > .chat-response-summary"),
      ...document.querySelectorAll(".chat-step-fold.alarm > .chat-step-summary"),
      ...document.querySelectorAll(".chat-tool-group.alarm > .chat-tool-group-head"),
    ];
    const tab = document.querySelector(".agent-tab");
    const composer = document.querySelector(".chat-composer");
    const inputWrap = document.querySelector(".chat-input-wrap");
    const attach = document.querySelector("#chat-attach")?.getBoundingClientRect();
    const send = document.querySelector("#chat-send")?.getBoundingClientRect();
    const connectionRows = [...document.querySelectorAll(".chat-notice-row")].filter((node) => node.innerText.startsWith("model unreachable ·"));
    return {
      prose: prose.length,
      visibleProse: prose.filter((node) => node.getClientRects().length > 0).length,
      folds: folds.length,
      openFolds: folds.filter((node) => !node.hidden && node.getAttribute("aria-expanded") === "true").length,
      decided: decided.length,
      tallDecisions: decided.filter((node) => node.getBoundingClientRect().height > 21).length,
      pendingResolvedCards: [...document.querySelectorAll(".approval-card")].filter((node) => /allowed for this chat|allowed once|denied/i.test(node.innerText)).length,
      alarmsWithoutFailure: alarmSummaries.filter((node) => !/failed/.test(node.innerText)).map((node) => node.innerText),
      headerChatConsoleLinks: document.querySelectorAll('.shell-page[data-page="chat"],.shell-page[data-page="console"]').length,
      tabPresent: Boolean(tab),
      offline: tab?.querySelector(".agent-tab-robot")?.classList.contains("offline") || tab?.querySelector(".agent-state")?.classList.contains("offline") || false,
      replayComposerDisabled: document.querySelector("#chat-task")?.disabled && document.querySelector("#chat-send")?.disabled,
      pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      logOverflow: document.querySelector("#chat-log")?.scrollWidth - document.querySelector("#chat-log")?.clientWidth,
      agentSpeakerImages: document.querySelectorAll(".chat-agent .chat-speaker img").length,
      userSpeakerImages: document.querySelectorAll(".chat-user .chat-speaker img").length,
      connectionRows: connectionRows.length,
      nestedConnectionRows: [...document.querySelectorAll(".chat-response-notice")].filter((node) => node.innerText.startsWith("model unreachable ·")).length,
      composerLeftInset: Math.round(inputWrap.getBoundingClientRect().left - composer.getBoundingClientRect().left),
      composerRightInset: Math.round(composer.getBoundingClientRect().right - inputWrap.getBoundingClientRect().right),
      actionSizes: [attach, send].map((rect) => ({ width: Math.round(rect.width), height: Math.round(rect.height) })),
      attachmentAboveSubmit: attach.bottom <= send.top,
      textareaRightPadding: getComputedStyle(document.querySelector("#chat-task")).paddingRight,
      composerOperatorControl: Boolean(document.querySelector("#chat-run-as-you")),
    };
  });
  assert.ok(ui.prose > 0, JSON.stringify(ui));
  assert.equal(ui.visibleProse, ui.prose, JSON.stringify(ui));
  assert.ok(ui.folds > 0, JSON.stringify(ui));
  assert.equal(ui.openFolds, 0, JSON.stringify(ui));
  assert.equal(ui.tallDecisions, 0, JSON.stringify(ui));
  assert.equal(ui.pendingResolvedCards, 0, JSON.stringify(ui));
  assert.deepEqual(ui.alarmsWithoutFailure, [], JSON.stringify(ui));
  assert.equal(ui.headerChatConsoleLinks, 0, JSON.stringify(ui));
  assert.equal(ui.tabPresent, true, JSON.stringify(ui));
  assert.equal(ui.offline, Boolean(finalSession.model_unreachable), JSON.stringify(ui));
  assert.equal(ui.replayComposerDisabled, true, JSON.stringify(ui));
  assert.ok(ui.pageOverflow <= 0 && ui.logOverflow <= 0, JSON.stringify(ui));
  assert.ok(ui.agentSpeakerImages > 0, JSON.stringify(ui));
  assert.equal(ui.userSpeakerImages, 0, JSON.stringify(ui));
  assert.equal(ui.connectionRows > 0, Boolean(finalSession.model_unreachable), JSON.stringify(ui));
  assert.equal(ui.nestedConnectionRows, 0, JSON.stringify(ui));
  assert.deepEqual(ui.actionSizes, [{ width: 24, height: 24 }, { width: 24, height: 24 }], JSON.stringify(ui));
  assert.equal(ui.attachmentAboveSubmit, true, JSON.stringify(ui));
  assert.equal(ui.textareaRightPadding, "64px", JSON.stringify(ui));
  assert.equal(ui.composerOperatorControl, false, JSON.stringify(ui));
  assert.equal(ui.composerLeftInset, 8, JSON.stringify(ui));
  assert.equal(ui.composerRightInset, 8, JSON.stringify(ui));
  const shellGeometry = () => page.evaluate(() => Object.fromEntries([
    ["shell", "#app-shell"],
    ["tabs", ".agent-tabs"],
    ["wrap", ".agent-tab-wrap"],
    ["tab", ".agent-tab"],
    ["plus", ".agent-tab-new"],
    ["plan", ".shell-page"],
    ["settings", ".shell-settings"],
  ].map(([key, selector]) => {
    const rect = document.querySelector(selector).getBoundingClientRect();
    return [key, { x: rect.x, y: rect.y, width: rect.width, height: rect.height }];
  })));
  const chatGeometry = await shellGeometry();
  if (finalSession.model_unreachable) await page.locator(".chat-notice-row").filter({ hasText: "model unreachable ·" }).scrollIntoViewIfNeeded();
  await page.screenshot({ path: join(args.evidence, "real-tape-chat.png") });

  const firstFold = page.locator(".chat-step-summary").first();
  const proseBefore = await page.locator(".chat-response-prose").allTextContents();
  await firstFold.click();
  assert.equal(await firstFold.getAttribute("aria-expanded"), "true");
  assert.deepEqual(await page.locator(".chat-response-prose").allTextContents(), proseBefore);
  await firstFold.click();

  const tab = page.locator(".agent-tab").first();
  await tab.click({ button: "right" });
  const historyRows = page.locator(".agent-tab-wrap .agent-chat-row");
  assert.equal(await historyRows.count(), Object.keys(finalState.sessions || {}).length);
  await page.screenshot({ path: join(args.evidence, "real-tape-menu.png") });
  await page.keyboard.press("Escape");
  await tab.click();
  // Item 2gk (v1.2.3): the numbers are two sections of Settings now, reached by
  // the gear. A replayed tape still fills them; that is what is checked here.
  await page.locator(".shell-settings").click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
  await page.locator(".settings-nav button", { hasText: "Activity" }).click();
  await page.locator("#activity-panel").waitFor({ state: "visible" });
  const panelGeometry = await shellGeometry();
  const activityCapabilities = await page.evaluate(() => {
    const visible = (selector) => {
      const node = document.querySelector(selector);
      if (!node) return false;
      const style = getComputedStyle(node);
      const box = node.getBoundingClientRect();
      return !node.hidden && style.display !== "none" && style.visibility !== "hidden" && box.width > 0 && box.height > 0;
    };
    return {
      groups: [...document.querySelectorAll("#activity-panel > .panel-group > .panel-caption > span:first-child")].map((node) => node.textContent.trim()),
      visible: Object.fromEntries(["#panel-lifetime", "#panel-live-content", "#rail", "#flow", ".rack-well", "#state-list", "#timeline-list", "#clear-stats", "#flush-memory"].map((selector) => [selector, visible(selector)])),
      pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      fixedHeaderHeight: document.querySelector("#app-shell").getBoundingClientRect().height,
      toolCounts: document.querySelectorAll("#panel-tool-counters .panel-line").length,
      flowRows: document.querySelectorAll("#flow .activity-row").length,
    };
  });
  assert.deepEqual(activityCapabilities.groups, ["Tool use", "Lifetime", "Live run", "Reflection", "Maintenance"], JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.visible["#panel-lifetime"], true, JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.visible["#clear-stats"], true, JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.visible["#flush-memory"], true, JSON.stringify(activityCapabilities));
  assert.ok(activityCapabilities.pageOverflow <= 0, JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.fixedHeaderHeight, chatGeometry.shell.height, JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.toolCounts, 0, JSON.stringify(activityCapabilities));
  assert.equal(activityCapabilities.flowRows, 0, JSON.stringify(activityCapabilities));
  await page.screenshot({ path: join(args.evidence, "real-tape-activity.png") });
  const panelWidths = [];
  for (const width of [820, 520, 320]) {
    await page.setViewportSize({ width, height: 975 });
    const layout = await page.evaluate(() => ({
      width: document.documentElement.clientWidth,
      overflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      groups: [...document.querySelectorAll("#activity-panel > .panel-group > .panel-caption > span:first-child")].filter((node) => getComputedStyle(node).display !== "none").map((node) => node.textContent.trim()),
      stateDisplay: getComputedStyle(document.querySelector(".state-well")).display,
      historyDisplay: getComputedStyle(document.querySelector(".timeline-well")).display,
      liveVisible: document.querySelector("#panel-live-content").getClientRects().length > 0,
      historyOverflow: document.querySelector(".timeline-well").scrollWidth - document.querySelector(".timeline-well").clientWidth,
      timelineOverlaps: [...document.querySelectorAll(".timeline-head")].reduce((count, head) => {
        const boxes = [...head.children].map((node) => node.getBoundingClientRect()).filter((box) => box.width > 0 && box.height > 0);
        return count + boxes.reduce((rowCount, box, index) => rowCount + boxes.slice(index + 1).filter((other) => box.left < other.right && box.right > other.left && box.top < other.bottom && box.bottom > other.top).length, 0);
      }, 0),
    }));
    assert.equal(layout.overflow, 0, JSON.stringify(layout));
    assert.deepEqual(layout.groups, activityCapabilities.groups, JSON.stringify(layout));
    if (activityCapabilities.flowRows > 0) {
      assert.notEqual(layout.stateDisplay, "none", JSON.stringify(layout));
      assert.notEqual(layout.historyDisplay, "none", JSON.stringify(layout));
      assert.equal(layout.liveVisible, true, JSON.stringify(layout));
    }
    assert.ok(layout.historyOverflow <= 0, JSON.stringify(layout));
    assert.equal(layout.timelineOverlaps, 0, JSON.stringify(layout));
    await page.screenshot({ path: join(args.evidence, `real-tape-panel-${width}.png`) });
    const bottom = await page.evaluate(() => {
      const surface = document.querySelector(".settings-content");
      surface.scrollTop = surface.scrollHeight;
      const box = document.querySelector(".panel-maintenance").getBoundingClientRect();
      return { scrollTop: surface.scrollTop, scrollHeight: surface.scrollHeight, maintenanceTop: box.top, maintenanceBottom: box.bottom, maintenanceReachable: box.top >= 32 && box.bottom <= innerHeight };
    });
    panelWidths.push({ ...layout, ...bottom });
    await page.screenshot({ path: join(args.evidence, `real-tape-panel-${width}-bottom.png`) });
    await page.locator(".settings-content").evaluate((surface) => { surface.scrollTop = 0; });
  }
  await page.setViewportSize({ width: 1250, height: 975 });
  await page.locator(".agent-tab").first().click();
  await page.locator("#settings-page").waitFor({ state: "hidden" });
  await page.locator(".chat-entry").first().waitFor();
  const returnedChatGeometry = await shellGeometry();
  if (args["expect-stable-shell"] === "true") {
    assert.deepEqual(panelGeometry, chatGeometry, JSON.stringify({ chatGeometry, panelGeometry }));
    assert.deepEqual(returnedChatGeometry, chatGeometry, JSON.stringify({ chatGeometry, returnedChatGeometry }));
  }
  assert.deepEqual(pageErrors, []);
  const chatFailedResponses = [...failedResponses];
  assert.ok(chatFailedResponses.every((response) =>
    (response.status === 404 && response.url.includes("/api/files/")) ||
    (response.status === 409 && response.url.includes("/api/")) ||
    (response.status === 501 && response.url.includes("/api/operator-files"))), JSON.stringify(chatFailedResponses));
  const chatConsoleErrors = [...consoleErrors];
  assert.equal(chatConsoleErrors.length, chatFailedResponses.length, JSON.stringify({ chatConsoleErrors, chatFailedResponses }));
  const settingsConsoleErrors = [];
  const settingsFailedResponses = [];
  const report = {
    result: "PASS streaming replay",
    tape: args.replay,
    records: recordCount,
    projectedChatEntries: finalSession.chat?.length || 0,
    condensedToolRowsObserved: result.toolKeys.length,
    condensedToolRowRemounts: result.remounts.length,
    projectionPatchesObserved: result.patchEvents,
    chatRenderFailureErrors: result.renderErrors.length,
    mountedRenderFailures: result.mountedRenderFailures,
    pageErrors,
    consoleErrors: chatConsoleErrors,
    settingsConsoleErrors,
    settingsFailedResponses,
    chatFailedResponses,
    ui,
    activityCapabilities,
    panelWidths,
    shellGeometry: { chat: chatGeometry, console: panelGeometry, returned_chat: returnedChatGeometry },
    plusUsableHitTarget: chatGeometry.plus.width >= 20 && chatGeometry.plus.height >= 20,
    cursor: result.cursor,
  };
  await writeFile(join(args.evidence, "result.json"), `${JSON.stringify(report, null, 2)}\n`);
  process.stdout.write(`${JSON.stringify(report, null, 2)}\n`);
} finally {
  await browser?.close().catch(() => {});
  for (const child of children.reverse()) { try { child.kill(); } catch {} }
  await sleep(250);
}
