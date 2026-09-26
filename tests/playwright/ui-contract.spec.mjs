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
      <div id="chat-status-strip" class="chat-status-strip"><span class="chat-notice">idle</span></div>
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
