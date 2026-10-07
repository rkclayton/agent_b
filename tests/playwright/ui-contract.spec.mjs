import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));

const appCSS = await readFile(new URL("../../web/css/app.css", import.meta.url), "utf8");
const chatJS = await readFile(new URL("../../web/js/chat.js", import.meta.url), "utf8");
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

test("OCR progress and Stop remain visible on the attachment chip", async ({ page }) => {
  expect(chatJS).toContain("OCR · page ${upload.page} of ${upload.total}");
  expect(chatJS).toContain('/api/attachments/stop');
  await page.setViewportSize({ width: 320, height: 180 });
  await page.setContent(`<!doctype html><style>:root{--ink:#D8DDE3;--well:#15181C;--trace:#F2B233;--alarm:#E4624F;--sans:sans-serif}${chatCSS}</style>
    <div class="chat-pending-attachments"><span class="chat-pending-file"><span>phone-scan.pdf · OCR · page 10 of 60</span><button>Stop</button></span></div>`);
  await expect(page.getByText("phone-scan.pdf · OCR · page 10 of 60")).toBeVisible();
  await expect(page.getByRole("button", { name: "Stop" })).toBeVisible();
  const shot = await page.screenshot();
  expect(shot.length, "the off-screen OCR chip screenshot is empty").toBeGreaterThan(0);
  expect(await page.locator(".chat-pending-file").evaluate((node) => node.getBoundingClientRect().right <= innerWidth)).toBe(true);
});

// The standing UI contract, in the operator's words: "nothing scrolls inside
// something that already scrolls — the chat transcript scrolls because it must, and
// a top-level settings page scrolls when its content genuinely needs it, but no
// panel, list or fold inside them gets its own scrollbar."
//
// Item 2l7 (f) asks for exactly this assertion over the Activity page, which is the
// adopted #activity-panel inside the Settings sheet.
function activityMarkup() {
  const start = indexHTML.indexOf('<div id="activity-panel"');
  // The end is the NEXT thing in the holder, whatever it is. Item 2no put the Plan's
  // own markup after this panel, and a slice that ran to the end of the document
  // dragged the Plan's textarea in and reported it as an Activity offender.
  const boundaries = ['<!-- Item 2no', '<div id="plan-panel"', '<div id="dissolved-sources"']
    .map((marker) => indexHTML.indexOf(marker, start))
    .filter((index) => index > start);
  const end = boundaries.length ? Math.min(...boundaries) : indexHTML.indexOf("</div>\n</body>", start);
  return indexHTML.slice(start, end > start ? end : indexHTML.length);
}

for (const width of [1280, 760]) {
  test(`nothing inside the Activity page scrolls independently at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await page.setContent(`<!doctype html><html><head><style>
      :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
        --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
        --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
      ${appCSS}
    </style></head><body>
      <div id="settings-page" class="settings-page">
        <div class="settings-layout"><div class="settings-content">${activityMarkup()}</div></div>
      </div>
    </body></html>`);

    const offenders = await page.evaluate(() => {
      const page = document.querySelector(".settings-content");
      const scrolls = (node) => {
        const style = getComputedStyle(node);
        return ["overflow", "overflow-y"].some((property) => ["auto", "scroll"].includes(style.getPropertyValue(property)));
      };
      return [...page.querySelectorAll("*")].filter(scrolls).map((node) => {
        const id = node.id ? `#${node.id}` : "";
        return `${node.tagName.toLowerCase()}${id}.${[...node.classList].join(".")}`;
      });
    });
    expect(offenders, "these elements inside the Activity page scroll on their own").toEqual([]);
  });
}

// Controls are the sheet's normal size; nothing oversized. The row actions 2l5 adds
// are the smallest controls on the sheet and must not exceed a normal row's height.
test("the connection row actions are the sheet's normal control size", async ({ page }) => {
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --alarm:#E4624F; --mono:monospace; --s1:4px; --s2:8px; }
    ${appCSS}
  </style></head><body>
    <div class="connection-row"><span class="connection-actions">
      <button class="row-action">s</button>
      <button class="row-action test">test</button>
      <button class="row-action">d</button>
      <button class="row-action">x</button>
    </span></div>
  </body></html>`);
  const sizes = await page.$$eval(".row-action", (nodes) => nodes.map((node) => {
    const box = node.getBoundingClientRect();
    return { width: box.width, height: box.height };
  }));
  expect(sizes).toHaveLength(4);
  for (const size of sizes) {
    expect(size.height, "a row action is taller than a settings row").toBeLessThanOrEqual(24);
    expect(size.height).toBeGreaterThan(0);
    expect(size.width).toBeGreaterThan(0);
  }
});

// Item 2le (f): both images resolve and the header text is present. The operator
// supplied the artwork and asked for "a very brief description adjacent that
// explains how planning works".
test("the Plan header carries the artwork and three lines about planning", async ({ page }) => {
  // Item 2no: the Plan section lives in the one document now.
  const planHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");
  const intro = planHTML.slice(planHTML.indexOf('<div class="plan-intro">'), planHTML.indexOf("</div>", planHTML.indexOf('<div class="plan-intro">')));
  expect(intro, "the Plan page has no header block").toContain("/static/assets/plan-mark.png");
  const copy = /<p>([\s\S]*?)<\/p>/.exec(intro)?.[1] ?? "";
  const sentences = (copy.match(/[^.]+\./g) || []).filter((value) => value.trim().length > 20);
  expect(sentences.length, "the description should be three sentences, no more").toBeLessThanOrEqual(3);
  expect(copy).toContain("A plan points at one repository");

  // Item 2lm (b) and (f): the nav mark is a size set now, because the strip draws
  // a 12px box and the product runs at more than one device pixel ratio --
  // rel-1.16.0/W0 measured the operator's own display at 1.75x, where that box is
  // 21 physical pixels while every screenshot captures it at 12. Each member is
  // prepared from the full-detail mark rather than reduced from the one above it,
  // and each must resolve as a real image at its own size.
  const assets = ["plan-mark-nav.png", "plan-mark-nav@2x.png", "plan-mark-nav@3x.png", "plan-mark.png"];
  const encoded = await Promise.all(assets.map(async (name) =>
    (await readFile(new URL(`../../web/assets/${name}`, import.meta.url))).toString("base64")));
  await page.setContent(encoded.map((data, index) => `<img id="a${index}" src="data:image/png;base64,${data}">`).join(""));
  const sizes = await page.evaluate(async () => {
    await Promise.all([...document.images].map((image) => image.decode()));
    return [...document.images].map((image) => [image.naturalWidth, image.naturalHeight]);
  });
  expect(sizes).toEqual([[12, 12], [24, 24], [36, 36], [128, 128]]);
});

// Item 2ld. The operator, 2026-09-26: "can we make it where if you grab this
// little part at the top of the chat window in between where you type and it
// displays that you can resize the chat input? and remove the [expand control]".
// Item 2oc: the input row goes all the way to zero while the strip stays as the
// reversible handle, and zero is remembered just like every other height.
test("the composer collapses to zero, remembers it, and opens from the same handle", async ({ page }) => {
  const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
  const body = `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --mono:monospace; }
    body { margin:0 } #wrap { height:600px; display:grid; grid-template-rows: 1fr auto auto }
    #chat-log { overflow-y:auto } ${chatCSS}
  </style></head><body><div id="wrap">
    <main id="chat-log"><p>transcript</p></main>
    <footer id="chat-composer" class="chat-composer">
      <div id="chat-resize-handle" class="chat-resize-handle" role="separator" title="Drag to resize the message box"></div>
      <div class="chat-composer-row"><div class="chat-input-wrap"><textarea id="chat-input"></textarea><span class="chat-input-actions"><button id="chat-mic" class="composer-control">mic</button><button id="chat-send" class="composer-control">send</button></span></div></div>
    </footer>
  </div>
  <script type="module">
    import { installComposerResize, COMPOSER_MIN } from "/js/composer-resize.js";
    window.COMPOSER_MIN = COMPOSER_MIN;
    installComposerResize({ handle: document.getElementById("chat-resize-handle"), composer: document.getElementById("chat-composer"), input: document.getElementById("chat-input"), log: document.getElementById("chat-log") });
  </script></body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer.html");
  await page.fill("#chat-input", "still here");

  const floor = await page.evaluate(() => window.COMPOSER_MIN);
  expect(floor, "the floor is a true zero").toBe(0);

  // Drag far past the bottom of the window: the clamp, not the pointer, decides.
  const strip = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2 + 900, { steps: 8 });
  await page.mouse.up();

  const applied = await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height")));
  expect(applied, "the composer did not reach its zero floor").toBe(floor);
  // The row and its controls are truly gone, while the typed value is retained.
  await expect(page.locator("#chat-input")).toHaveValue("still here");
  expect((await page.locator("#chat-input").boundingBox()).height).toBe(0);
  expect((await page.locator(".chat-composer-row").boundingBox()).height, "the controls still occupied a row").toBe(0);
  const collapsed = await page.locator("#chat-composer").boundingBox();
  const collapsedStrip = await page.locator("#chat-resize-handle").boundingBox();
  expect(collapsed.y + collapsed.height, "space remained below the handle").toBe(collapsedStrip.y + collapsedStrip.height);
  // The handle is still findable: the strip is still there and still the target.
  const smallest = await page.locator("#chat-resize-handle").boundingBox();
  expect(smallest.height, "the handle disappeared at the smallest size").toBe(4);

  await page.reload();
  expect(await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height"))), "zero was not restored after reload").toBe(0);
  expect((await page.locator("#chat-input").boundingBox()).height).toBe(0);
  expect((await page.locator(".chat-composer-row").boundingBox()).height).toBe(0);

  // And it comes back: the drag is reversible from the floor.
  const restoredStrip = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(restoredStrip.x + 40, restoredStrip.y + restoredStrip.height / 2);
  await page.mouse.down();
  await page.mouse.move(restoredStrip.x + 40, restoredStrip.y + restoredStrip.height / 2 - 100, { steps: 6 });
  await page.mouse.up();
  const back = await page.evaluate(() => Number.parseFloat(document.getElementById("chat-composer").style.getPropertyValue("--composer-height")));
  expect(back, "the composer could not be dragged back up from the floor").toBeGreaterThan(floor);
  await page.fill("#chat-input", "usable again");
  await expect(page.locator("#chat-input")).toHaveValue("usable again");
});

test("dragging the strip resizes the composer, keeps what is typed, and persists", async ({ page }) => {
  const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
  const body = `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --mono:monospace; }
    body { margin:0 } #wrap { height:600px; display:grid; grid-template-rows: 1fr auto auto }
    #chat-log { overflow-y:auto } ${chatCSS}
  </style></head><body><div id="wrap">
    <main id="chat-log"><p>transcript</p></main>
    <div id="chat-resize-handle" class="chat-resize-handle" role="separator" title="Drag to resize the message box"></div>
    <footer id="chat-composer" class="chat-composer"><textarea id="chat-input"></textarea></footer>
  </div>
  <script type="module">
    import { installComposerResize } from "/js/composer-resize.js";
    installComposerResize({ handle: document.getElementById("chat-resize-handle"), composer: document.getElementById("chat-composer"), input: document.getElementById("chat-input"), log: document.getElementById("chat-log") });
  </script></body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer.html");

  await page.fill("#chat-input", "a message I am still writing");
  const strip = await page.locator("#chat-resize-handle").boundingBox();
  const before = (await page.locator("#chat-input").boundingBox()).height;

  // Drag the strip UP: the message box grows.
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2 - 120, { steps: 6 });
  await page.mouse.up();
  const taller = (await page.locator("#chat-input").boundingBox()).height;
  expect(taller, "dragging up did not make the composer taller").toBeGreaterThan(before + 60);

  // And back DOWN.
  const strip2 = await page.locator("#chat-resize-handle").boundingBox();
  await page.mouse.move(strip2.x + 40, strip2.y + strip2.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip2.x + 40, strip2.y + strip2.height / 2 + 90, { steps: 6 });
  await page.mouse.up();
  const shorter = (await page.locator("#chat-input").boundingBox()).height;
  expect(shorter, "dragging down did not make the composer shorter").toBeLessThan(taller);

  // (e): nothing typed is lost, and the transcript was never scrolled.
  await expect(page.locator("#chat-input")).toHaveValue("a message I am still writing");
  expect(await page.evaluate(() => document.getElementById("chat-log").scrollTop)).toBe(0);

  // (c): the height is remembered for this surface.
  const [remembered, applied] = await page.evaluate(() => [
    localStorage.getItem("agentb.composer-height"),
    document.getElementById("chat-composer").style.getPropertyValue("--composer-height"),
  ]);
  expect(Number(remembered)).toBeGreaterThan(0);
  // What was remembered is the height that was applied; the rendered textarea is
  // that plus its own padding.
  expect(Number(remembered)).toBe(Number.parseFloat(applied));
  expect(shorter).toBeGreaterThanOrEqual(Number(remembered));
});

// Item 2ps / I12: retained history must not make the first usable browser frame
// grow with what is stored. This runs the real document and modules in headless
// Edge; only the server-authored event stream is replaced with a deterministic
// snapshot so the two retained-size arms differ in no other way.
test("first usable frame does not grow with retained chat history", async ({ browser }) => {
  test.setTimeout(180_000);
  const fixture = (chats, entries) => {
    const sessions = {};
    for (let chat = 0; chat < chats; chat++) {
      const id = `chat-${chat}`;
      const retained = chat === 0 ? entries : 0;
      const visible = Math.min(50, retained);
      const start = retained - visible;
      const timeline = Array.from({ length: visible }, (_, index) => ({ type: "tool.result", session_id: id, run_id: "run", data: { call_id: start + index, result: "fixture" } }));
      const transcript = Array.from({ length: visible }, (_, index) => ({ type: (start + index) % 2 ? "agent" : "user", key: `entry:${start + index}`, text: "fixture", done: true }));
      sessions[id] = { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: entries + 1 }, complete: true, id, label: id,
        role: "b", created_at: "2026-10-01T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] },
        timeline, chat: transcript, history_start: start, history_end: retained, history_total: retained, runnable: true, closed: false };
    }
    return { sessions, connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {} };
  };
  const measure = async (state) => {
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
    }, { snapshot: state });
    const page = await context.newPage();
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
      if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
      const relative = url.pathname.replace(/^\/static\//, "");
      return route.fulfill({ path: webRoot + relative });
    });
    await page.goto("http://first-frame.test/chat?setup=skip&session=chat-0", { waitUntil: "domcontentloaded" });
    await page.waitForFunction(() => {
      const input = document.querySelector("#chat-task");
      return input && !input.disabled;
    });
    const elapsed = await page.evaluate(() => performance.now());
    await context.close();
    return elapsed;
  };
  const median = (values) => [...values].sort((a, b) => a - b)[Math.floor(values.length / 2)];
  const smallState = fixture(1, 0);
  const largeState = fixture(30, 3000);
  const small = [], large = [];
  for (let index = 0; index < 5; index++) {
    small.push(await measure(smallState));
    console.log(`2ps empty sample ${index + 1}=${small.at(-1).toFixed(1)}ms`);
    large.push(await measure(largeState));
    console.log(`2ps large sample ${index + 1}=${large.at(-1).toFixed(1)}ms`);
  }
  const smallMedian = median(small), largeMedian = median(large);
  console.log(`2ps first usable frame empty=${smallMedian.toFixed(1)}ms large=${largeMedian.toFixed(1)}ms ratio=${(largeMedian / smallMedian).toFixed(2)}x`);
  expect(largeMedian, `empty=${smallMedian.toFixed(1)}ms large=${largeMedian.toFixed(1)}ms`).toBeLessThanOrEqual(2 * smallMedian);
});

test("fresh context leaves every transcript entry visible and adds its searchable-history line", async ({ browser }) => {
  const id = "chat-fresh";
  const snapshot = {
    sessions: { [id]: { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: 4 }, complete: true, id, label: "long chat",
      role: "b", created_at: "2026-10-05T00:00:00Z", run: { status: "idle" }, tools: [], messages: [], budget: {}, activity: { completed_stages: [] },
      timeline: [], chat: [
        { type: "user", key: "message:u1", text: "FIRST VISIBLE TURN" },
        { type: "agent", key: "turn:r1:1", text: "SECOND VISIBLE TURN", done: true },
        { type: "notice", key: "event:3", run_id: "r1", event: { seq: 3, type: "compaction", session_id: id, run_id: "r1", data: { kind: "fresh", before: 1000, after: 100 } } },
      ], runnable: true, closed: false } },
    connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {},
  };
  const context = await browser.newContext({ viewport: { width: 1250, height: 975 } });
  await context.addInitScript(({ snapshot, id }) => {
    sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: id, surface: { kind: "chat", key: id } }));
    class FixtureEvents {
      constructor() { this.listeners = new Map(); setTimeout(() => { this.onopen?.(); this.emit("snapshot", { type: "snapshot", data: snapshot }); }); }
      addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
      emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); }
      close() {}
    }
    globalThis.EventSource = FixtureEvents;
  }, { snapshot, id });
  const page = await context.newPage();
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
    if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
    return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
  });
  await page.goto(`http://fresh-context.test/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
  const log = page.locator("#chat-log");
  await expect(log).toContainText("FIRST VISIBLE TURN");
  await expect(log).toContainText("SECOND VISIBLE TURN");
  await expect(log).toContainText("fresh context — earlier turns searchable");
  await expect(log).not.toContainText("Fresh-context hand-off:");
  await context.close();
});

test("2ql finished and live s56-shaped replays stay compact and keep every entry reachable", async ({ browser }) => {
  const id = "chat-2ql";
  const runID = "run-2ql";
  const replay = [
    { type: "user", key: "user-2ql", text: "Review the synthetic build record." },
    { type: "agent", key: "narration-one", run_id: runID, text: "Checked the build inputs.", reasoning: "Compared the recorded inputs.", reasoningTokens: 7, done: true, thinkingMS: 40 },
    { type: "tool", key: "failed-then-retried", run_id: runID, name: "shell", args: { command: "git stash pop --index synthetic-extra" }, result: { ok: false, error_class: "exit_nonzero", ms: 12 }, content: "synthetic conflict" },
    { type: "tool", key: "retry-succeeded", run_id: runID, name: "shell", args: { command: "git stash pop --index synthetic-extra" }, result: { ok: true, ms: 18 }, content: "synthetic success" },
    { type: "agent", key: "narration-two", run_id: runID, text: "Verified the rebuilt output.", reasoning: "Read the synthetic verification result.", reasoningTokens: 11, done: true, thinkingMS: 55 },
    { type: "tool", key: "final-tool", run_id: runID, name: "read_file", args: { path: "C:\\synthetic\\evidence\\result.txt" }, result: { ok: true, ms: 8 }, content: "synthetic proof" },
    { type: "agent", key: "final-answer", run_id: runID, text: "The synthetic release proof is complete.", done: true },
  ];
  const snapshot = (status) => {
    const chat = status === "running" ? replay.slice(0, -1) : replay;
    return ({
    sessions: { [id]: { schema_version: 1, cursor: { generation: `${id}.jsonl`, offset: chat.length }, complete: true, id, label: "Synthetic replay",
      agent_id: "agent_b", role: "b", created_at: "2026-10-05T00:00:00Z", run: { status, run_id: runID }, tools: [], messages: [], budget: {},
      activity: status === "running" ? { completed_stages: [], started_at: Date.now(), stage: "execute", stage_state: "enter", active_tool: "read_file", tool_target: "result.txt" } : { completed_stages: [] }, timeline: [], chat, runnable: true, closed: false } },
    connections: [], config: { agents: [{ name: "agent_b", b: "fixture" }], connections: [] }, flow: { stages: [], edges: [] }, tools: [], plans: [], profiles: { active: "", names: [] }, build: {},
  }); };
  const open = async (status, host) => {
    const context = await browser.newContext({ viewport: { width: 1000, height: 760 } });
    await context.addInitScript(({ state, id }) => {
      sessionStorage.setItem("agentb.selection", JSON.stringify({ agent_id: "agent_b", session_id: id, surface: { kind: "chat", key: id } }));
      class FixtureEvents {
        constructor() { this.listeners = new Map(); globalThis.__fixtureEvents = this; setTimeout(() => this.emit("snapshot", { type: "snapshot", data: state })); }
        addEventListener(type, listener) { this.listeners.set(type, [...(this.listeners.get(type) || []), listener]); }
        emit(type, value) { for (const listener of this.listeners.get(type) || []) listener({ data: JSON.stringify(value) }); }
        close() {}
      }
      globalThis.EventSource = FixtureEvents;
    }, { state: snapshot(status), id });
    const page = await context.newPage();
    await page.route("**/*", async (route) => {
      const url = new URL(route.request().url());
      if (url.pathname === "/chat") return route.fulfill({ contentType: "text/html", body: indexHTML });
      if (url.pathname.startsWith("/api/")) return route.fulfill({ contentType: "application/json", body: "{}" });
      return route.fulfill({ path: webRoot + url.pathname.replace(/^\/static\//, "") });
    });
    await page.goto(`http://${host}/chat?setup=skip&session=${id}`, { waitUntil: "domcontentloaded" });
    await expect(page.locator("#chat-log")).toContainText(status === "running" ? "Verified the rebuilt output" : "synthetic release proof");
    return { context, page };
  };

  const finished = await open("idle", "finished-2ql.test");
  const worked = finished.page.locator(".chat-work-summary");
  await expect(worked).toHaveCount(1);
  await expect(worked).toContainText(/Worked .* · 2 steps ▸/);
  await expect(finished.page.getByText("The synthetic release proof is complete.")).toBeVisible();
  await expect(finished.page.getByText("Checked the build inputs.")).toHaveCount(0);
  await expect(finished.page.locator("#chat-status-line")).toHaveCount(0);
  await finished.page.screenshot({ path: "test-results/2ql-s56-finished.png", fullPage: true });
  await finished.page.screenshot({ path: "test-results/2qm-idle.png", fullPage: true });
  await worked.click();
  await expect(finished.page.getByText("Checked the build inputs.")).toBeVisible();
  await expect(finished.page.locator(".chat-narration-line .chat-step-summary")).toHaveCount(2);
  await expect(finished.page.locator(".chat-step-fold.alarm")).toHaveCount(0);
  await expect(finished.page.locator(".chat-step-summary").filter({ hasText: "retried" })).toContainText("1 retried");
  await expect(finished.page.getByText("thinking · 7 tokens")).toBeVisible();
  await expect(finished.page.locator(".tool-tick").first()).toContainText(/shell\s*·\s*git stash pop/);
  await finished.page.evaluate(() => document.querySelector("#chat-log").insertAdjacentHTML("beforeend", '<section class="chat-entry chat-summary"><div class="chat-speaker" aria-label="summary"><span aria-hidden="true">summary</span></div><div class="chat-content">summary row</div></section><section class="chat-entry chat-notice-row"><div class="chat-content">harness note</div></section>'));
  await finished.page.setViewportSize({ width: 1400, height: 900 });
  const rail = async (scale) => finished.page.evaluate((value) => {
    document.querySelector(".chat-page").style.setProperty("--chat-scale", value);
    const probe = Object.assign(document.createElement("i"), { textContent: "x" }); probe.style.cssText = "position:absolute;font-size:var(--ct)"; document.body.append(probe);
    const rows = [...document.querySelectorAll("#chat-log > .chat-entry")].map((row) => ({ kind: row.className, track: parseFloat(getComputedStyle(row).gridTemplateColumns), left: row.querySelector(":scope > .chat-content")?.getBoundingClientRect().left }));
    const speaker = document.querySelector(".chat-agent > .chat-speaker"), label = speaker.lastElementChild, robot = speaker.querySelector("img"), user = document.querySelector(".chat-user > .chat-speaker");
    const result = { rows, base: parseFloat(getComputedStyle(probe).fontSize), labelSize: parseFloat(getComputedStyle(label).fontSize), labelWidth: label.getBoundingClientRect().width,
      labelWhole: label.scrollWidth <= label.clientWidth + 1, robotWidth: robot.getBoundingClientRect().width, centers: Math.abs(label.getBoundingClientRect().x + label.getBoundingClientRect().width / 2 - robot.getBoundingClientRect().x - robot.getBoundingClientRect().width / 2),
      userText: user.innerText, userName: user.getAttribute("aria-label") }; probe.remove(); return result;
  }, scale);
  const defaultRail = await rail(1); console.log("2r2 default rail", JSON.stringify(defaultRail));
  expect(new Set(defaultRail.rows.map((row) => row.track)).size).toBe(1); expect(new Set(defaultRail.rows.map((row) => row.left)).size).toBe(1);
  expect(Math.abs(defaultRail.rows[0].track - Math.max(defaultRail.robotWidth, defaultRail.labelWidth))).toBeLessThanOrEqual(2); expect(defaultRail.labelSize).toBeCloseTo(defaultRail.base * 2 / 3, 1);
  expect(defaultRail.centers).toBeLessThanOrEqual(1); expect({ text: defaultRail.userText, name: defaultRail.userName }).toEqual({ text: "", name: "you" });
  await finished.page.screenshot({ path: "test-results/2r2-default.png", fullPage: true });
  for (const [name, scale] of [["smallest", .9], ["largest", 1.5]]) { const seen = await rail(scale); console.log(`2r2 ${name} rail`, JSON.stringify(seen)); expect(seen.labelWhole && seen.rows[0].track >= seen.robotWidth).toBe(true); await finished.page.screenshot({ path: `test-results/2r2-${name}.png`, fullPage: true }); }
  await finished.context.close();

  const live = await open("running", "live-2ql.test");
  await expect(live.page.locator(".chat-work-summary")).toHaveCount(0);
  const liveBlocks = live.page.locator(".chat-response-block");
  await expect(liveBlocks).toHaveCount(2);
  await expect(liveBlocks.nth(0).locator(".chat-step-rows > *")).toHaveCount(0);
  await expect(liveBlocks.nth(1).locator(".chat-step-rows > *")).not.toHaveCount(0);
  const liveStatus = live.page.locator("#chat-status-line");
  await expect(liveStatus).toBeVisible();
  await expect(liveStatus).toContainText(/read_file.*result\.txt/i);
  await expect(live.page.locator("#chat-log > :last-child")).toHaveAttribute("id", "chat-status-line");
  await expect(live.page.locator("#chat-status-strip")).toHaveCount(0);
  await expect(live.page.locator(".chat-input-actions > .chat-attach-wrap + #chat-mic")).toHaveCount(1);
  await live.page.evaluate((next) => globalThis.__fixtureEvents.emit("snapshot", { type: "snapshot", data: next }), (() => {
    const next = structuredClone(snapshot("running"));
    next.sessions[id].chat.push({ type: "agent", key: "later-line", run_id: runID, text: "A newer synthetic line arrived.", done: false });
    next.sessions[id].cursor.offset++;
    return next;
  })());
  await expect(live.page.getByText("A newer synthetic line arrived.")).toBeVisible();
  await expect(live.page.locator("#chat-log > :last-child")).toHaveAttribute("id", "chat-status-line");
  await live.page.screenshot({ path: "test-results/2ql-s56-live.png", fullPage: true });
  await live.page.screenshot({ path: "test-results/2qm-running.png", fullPage: true });
  await live.context.close();
});

// Item 2lj (f) and (g): the standing UI contract still holds at every step. At
// the largest size nothing overflows and nothing gains a scrollbar it did not
// have, at the wide width and at the phone viewport both.
const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");

function chatPage(scale, face) {
  return `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px;
      --agent-tab-width:118px; --etch-fade:.165; --chat-scale:${scale}; --chat-face:${face}; }
    ${appCSS}
    ${chatCSS}
  </style></head><body class="chat-page">
    <main id="chat-log" class="chat-log">
      <article class="chat-entry"><div class="chat-speaker">operator</div><div class="chat-content">
        <p>A message long enough to wrap at the phone width and to keep wrapping when the reader asks for the largest step.</p>
        <pre><code>read_file("C:/projects/agentb/internal/credential/store.go")</code></pre>
      </div></article>
    </main>
    <footer id="chat-composer" class="chat-composer">
      <div id="chat-resize-handle" class="chat-resize-handle"></div>
      <div class="chat-input-wrap"><textarea id="chat-input">typed text</textarea></div>
    </footer>
  </body></html>`;
}

for (const width of [1280, 390]) {
  for (const [step, scale] of [["smallest", 0.9], ["largest", 1.5]]) {
    test(`the chat surface holds the UI contract at the ${step} step at ${width}px`, async ({ page }) => {
      await page.setViewportSize({ width, height: 900 });
      await page.setContent(chatPage(scale, '"OpenDyslexic", var(--sans)'));
      const findings = await page.evaluate(() => {
        const problems = [];
        if (document.documentElement.scrollWidth > document.documentElement.clientWidth + 1) problems.push("the page scrolls sideways");
        for (const element of document.querySelectorAll(".chat-composer, .chat-composer *, .chat-entry, .chat-content")) {
          const style = getComputedStyle(element);
          // The transcript scrolls because it must; nothing inside it may.
          if (/(auto|scroll)/.test(style.overflowY) && element.scrollHeight > element.clientHeight + 1) {
            problems.push(`${element.className || element.tagName} scrolls on its own`);
          }
          if (element.scrollWidth > element.clientWidth + 1) problems.push(`${element.className || element.tagName} overflows sideways`);
        }
        return problems;
      });
      expect(findings, findings.join("; ")).toEqual([]);
    });
  }
}

// (e): a code block stays monospaced whatever the prose face is. The prose
// changes and the code does not, under every typeface the Chats page offers.
test("a code block stays monospaced under every typeface", async ({ page }) => {
  const faces = ["IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic", "Segoe UI", "Verdana", "Comic Sans MS"];
  for (const face of faces) {
    await page.setContent(chatPage(1, `"${face}", var(--sans)`));
    const seen = await page.evaluate(() => ({
      prose: getComputedStyle(document.querySelector(".chat-content p")).fontFamily,
      code: getComputedStyle(document.querySelector(".chat-content code")).fontFamily,
    }));
    expect(seen.prose, `${face} did not reach the prose`).toContain(face);
    expect(seen.code, `${face} leaked into a code block`).not.toContain(face);
    expect(seen.code).toContain("monospace");
  }
});

// Item 2lk (d): the same rule over EVERY settings page, not only Activity, so a
// new inner scroller is caught where it appears rather than the next time
// someone looks. The pages are rendered from their own source, so a selector
// that no page uses cannot hide here either.
const settingsSources = Object.fromEntries(await Promise.all(
  ["settings-security.js", "settings-general.js", "settings-workspace.js", "settings-profiles.js",
   "settings-connections.js", "settings-chats.js", "settings-notifications.js", "settings-about.js"]
    .map(async (name) => {
      try { return [name, await readFile(new URL(`../../web/js/${name}`, import.meta.url), "utf8")]; }
      catch { return [name, ""]; }
    })));

// Every class the settings pages put on an element, gathered from their own
// markup: whatever app.css says about any of them has to obey the contract.
const settingsClasses = [...new Set(
  Object.values(settingsSources).flatMap((source) => [...source.matchAll(/class="([^"$]+)"/g)]
    .flatMap((match) => match[1].split(/\s+/)))
)].filter(Boolean).sort();

test("no class any settings page uses is styled to scroll inside the page", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">
      ${settingsClasses.map((name) => `<div class="${name}" data-probe="${name}">text</div>`).join("")}
    </div></div></div>
  </body></html>`);

  const offenders = await page.evaluate(() => [...document.querySelectorAll("[data-probe]")].filter((node) => {
    const style = getComputedStyle(node);
    return ["overflow", "overflow-y"].some((property) => ["auto", "scroll"].includes(style.getPropertyValue(property)));
  }).map((node) => node.dataset.probe));

  expect(offenders, "these settings classes scroll inside a page that already scrolls").toEqual([]);
  // The probe is only worth anything if it actually covered the two this item
  // exists for, so it says so rather than passing on an empty list.
  expect(settingsClasses).toContain("settings-feedback");
  expect(settingsClasses).toContain("memory-content");
});

// (b): Activity leaves no empty column at 1250px.
test("Activity's upper grid leaves no empty column at 1250px", async ({ page }) => {
  await page.setViewportSize({ width: 1250, height: 900 });
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">${activityMarkup()}</div></div></div>
  </body></html>`);
  // Twenty metric rows on the right, two tool rows on the left: the shape that
  // produced the hole rel-1.14.0/W7 reported.
  await page.evaluate(() => {
    const line = (label, value) => `<div class="panel-line"><span>${label}</span><span>${value}</span></div>`;
    document.querySelector("#panel-stats").innerHTML =
      Array.from({ length: 20 }, (_, index) => line(`metric ${index}`, index)).join("");
    document.querySelector("#panel-tool-counters").innerHTML = line("list_dir", 1) + line("write_file", 1);
  });

  const geometry = await page.evaluate(() => {
    const pane = document.querySelector("#panel-tool-use").getBoundingClientRect();
    const lifetime = document.querySelector("#panel-lifetime").getBoundingClientRect();
    const rows = document.querySelector("#panel-lifetime .panel-lines");
    const columns = getComputedStyle(rows).gridTemplateColumns.split(" ").length;
    return { pane: pane.height, paneWidth: Math.round(pane.width), lifetime: lifetime.height, columns, width: Math.round(lifetime.width), sameRow: Math.abs(pane.top - lifetime.top) < 2 };
  });

  // Both panes take the whole surface, so there is no second column left to be
  // empty, and the metrics use that width instead of running down one column.
  expect(geometry.sameRow, "the panes still share a row").toBe(false);
  expect(geometry.columns, `the metrics still run down a single column in ${geometry.width}px`).toBeGreaterThan(1);
  expect(geometry.width, "Lifetime does not take the width of the surface").toBeGreaterThan(700);
  expect(geometry.paneWidth, "Tool use does not take the width of the surface").toBeGreaterThan(700);
});

// Item 2ln (e): rel-1.15.0 card 8 said the Agents page leaves a column empty at
// 1250px. [[2iq]] replaced that page with a role table in v1.16.0 and the card
// was carried forward UNMEASURED. This measures it, which is how the item says
// to close it: either the column is no longer empty, or it is fixed here.
test("the Agents page leaves no empty column at 1250px", async ({ page }) => {
  await page.setViewportSize({ width: 1250, height: 900 });
  const agentsMarkup = (() => {
    const start = indexHTML.indexOf('<div id="agents-panel"');
    const end = indexHTML.indexOf('<div id="activity-panel"', start);
    return indexHTML.slice(start, end > start ? end : indexHTML.length);
  })();
  await page.setContent(`<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --trace:#F2B233; --alarm:#E4624F;
      --accent-plan:#5AC8FA; --sans:sans-serif; --mono:monospace;
      --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s6:24px; --s8:32px; --app-header-height:32px; --agent-tab-width:118px; --etch-fade:.165; }
    ${appCSS}
  </style></head><body>
    <div id="settings-page" class="settings-page"><div class="settings-layout"><div class="settings-content">${agentsMarkup}</div></div></div>
  </body></html>`);

  // The shape that produced the hole: a role table of three rows beside a tool
  // list, in a two-column surface.
  await page.evaluate(() => {
    const row = (role, what) => `<div class="panel-role-row"><span class="panel-role-name">${role}</span><span class="panel-role-what">${what}</span><select><option>local</option></select><span class="panel-role-state">takes effect on the next run that reads it</span></div>`;
    document.querySelector("#panel-roles").innerHTML =
      row("b", "the one you talk to") + row("c", "the worker") + row("d", "the planner");
    document.querySelector("#panel-tools").innerHTML =
      Array.from({ length: 13 }, (_, i) => `<div class="panel-line"><span>tool ${i}</span><span>on</span></div>`).join("");
  });

  const geometry = await page.evaluate(() => {
    const surface = document.querySelector("#agents-panel");
    const kids = [...surface.children].map((node) => {
      const r = node.getBoundingClientRect();
      return { id: node.id || node.className, x: Math.round(r.left), w: Math.round(r.width), h: Math.round(r.height) };
    });
    return { surface: Math.round(surface.getBoundingClientRect().width), kids };
  });

  // Every child takes the width it is given: no child sits in one column of a
  // two-column surface with nothing beside it.
  const narrow = geometry.kids.filter((kid) => kid.w < geometry.surface - 4);
  expect(narrow, `these sit in a partial column: ${JSON.stringify(narrow)} of ${geometry.surface}px`).toEqual([]);
});
