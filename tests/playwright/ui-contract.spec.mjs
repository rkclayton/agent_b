import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));

const appCSS = await readFile(new URL("../../web/css/app.css", import.meta.url), "utf8");
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");

// The standing UI contract, in the operator's words: "nothing scrolls inside
// something that already scrolls — the chat transcript scrolls because it must, and
// a top-level settings page scrolls when its content genuinely needs it, but no
// panel, list or fold inside them gets its own scrollbar."
//
// Item 2l7 (f) asks for exactly this assertion over the Activity page, which is the
// adopted #activity-panel inside the Settings sheet.
function activityMarkup() {
  const start = indexHTML.indexOf('<div id="activity-panel"');
  const end = indexHTML.indexOf('<div id="dissolved-sources"', start) > 0
    ? indexHTML.indexOf('<div id="dissolved-sources"', start)
    : indexHTML.indexOf("</div>\n</body>", start);
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
  const planHTML = await readFile(new URL("../../web/plan.html", import.meta.url), "utf8");
  const intro = planHTML.slice(planHTML.indexOf('<div class="plan-intro">'), planHTML.indexOf("</div>", planHTML.indexOf('<div class="plan-intro">')));
  expect(intro, "the Plan page has no header block").toContain("/static/assets/plan-mark.png");
  const copy = /<p>([\s\S]*?)<\/p>/.exec(intro)?.[1] ?? "";
  const sentences = (copy.match(/[^.]+\./g) || []).filter((value) => value.trim().length > 20);
  expect(sentences.length, "the description should be three sentences, no more").toBeLessThanOrEqual(3);
  expect(copy).toContain("A plan points at one repository");

  // Both assets resolve as real images at the sizes they are drawn.
  await page.setContent(`<img id="nav" src="data:image/png;base64,${(await readFile(new URL("../../web/assets/plan-mark-nav.png", import.meta.url))).toString("base64")}">
    <img id="head" src="data:image/png;base64,${(await readFile(new URL("../../web/assets/plan-mark.png", import.meta.url))).toString("base64")}">`);
  const sizes = await page.evaluate(async () => {
    await Promise.all([...document.images].map((image) => image.decode()));
    return [...document.images].map((image) => [image.id, image.naturalWidth, image.naturalHeight]);
  });
  expect(sizes).toEqual([["nav", 24, 24], ["head", 128, 128]]);
});

// Item 2ld. The operator, 2026-09-26: "can we make it where if you grab this
// little part at the top of the chat window in between where you type and it
// displays that you can resize the chat input? and remove the [expand control]".
test("dragging the strip resizes the composer, keeps what is typed, and persists", async ({ page }) => {
  const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");
  const body = `<!doctype html><html><head><style>
    :root { --bezel:#2A2E35; --well:#15181C; --ink:#D8DDE3; --mute:#7D8794; --mono:monospace; }
    body { margin:0 } #wrap { height:600px; display:grid; grid-template-rows: 1fr auto auto }
    #chat-log { overflow-y:auto } ${chatCSS}
  </style></head><body><div id="wrap">
    <main id="chat-log"><p>transcript</p></main>
    <div id="chat-status-strip" class="chat-status-strip" role="separator" title="Drag to resize the message box">ready</div>
    <footer id="chat-composer" class="chat-composer"><textarea id="chat-input"></textarea></footer>
  </div>
  <script type="module">
    import { installComposerResize } from "/js/composer-resize.js";
    installComposerResize({ strip: document.getElementById("chat-status-strip"), composer: document.getElementById("chat-composer"), input: document.getElementById("chat-input"), log: document.getElementById("chat-log") });
  </script></body></html>`;
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/composer.html") return route.fulfill({ contentType: "text/html", body });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://composer.test/composer.html");

  await page.fill("#chat-input", "a message I am still writing");
  const strip = await page.locator("#chat-status-strip").boundingBox();
  const before = (await page.locator("#chat-input").boundingBox()).height;

  // Drag the strip UP: the message box grows.
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2);
  await page.mouse.down();
  await page.mouse.move(strip.x + 40, strip.y + strip.height / 2 - 120, { steps: 6 });
  await page.mouse.up();
  const taller = (await page.locator("#chat-input").boundingBox()).height;
  expect(taller, "dragging up did not make the composer taller").toBeGreaterThan(before + 60);

  // And back DOWN.
  const strip2 = await page.locator("#chat-status-strip").boundingBox();
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
