import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");

async function measure(page, bannerVisible) {
  await page.setContent(`
    <style>
      :root { --mute: #777; --ink: #eee; --mono: monospace; }
      ${chatCSS}
    </style>
    <div id="strip" class="chat-status-strip" style="width:800px">
      <span id="chat-notice">ready</span>
      <button id="chat-retry-model" hidden>Retry</button>
      <span id="banner" class="chat-update-banner" ${bannerVisible ? "" : "hidden"}>
        <span>Update available</span><button>Install</button><button>Dismiss</button>
      </span>
      <span id="attach" class="chat-attach-wrap"><button style="width:24px;height:24px">+</button></span>
    </div>
  `);
  return page.evaluate(() => {
    const strip = document.querySelector("#strip").getBoundingClientRect();
    const attach = document.querySelector("#attach").getBoundingClientRect();
    const banner = document.querySelector("#banner").getBoundingClientRect();
    return {
      attachRightGap: strip.right - attach.right,
      bannerAttachGap: attach.left - banner.right,
    };
  });
}

test("paperclip stays at the right edge with the update banner hidden or visible", async ({ page }) => {
  // Item 2me (c): this used to require the paperclip to sit HARD AGAINST the
  // window edge, which it only did because a -4px right margin cancelled the
  // strip's own right padding while the text on the left kept its 4px. Both ends
  // are inset the same now, so the gap is the strip's padding and the assertion
  // moved with the contract rather than being loosened.
  const hidden = await measure(page, false);
  expect(hidden.attachRightGap).toBe(4);

  const visible = await measure(page, true);
  expect(visible.attachRightGap).toBe(4);
  expect(visible.bannerAttachGap).toBeGreaterThanOrEqual(5);
  expect(visible.bannerAttachGap).toBeLessThanOrEqual(7);
});

// Item 2me (b), (c) and (d). The numbers here were MEASURED in the running app
// first — the strip was 28px, dropped to 20px with the paperclip removed, and its
// text was already exactly on the centre line. This renders the shipped markup
// and the shipped stylesheet in the page's own nesting so the same numbers are
// held to.
const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");
const indexHTML = await readFile(new URL("../../web/index.html", import.meta.url), "utf8");
const stripMarkup = indexHTML.slice(indexHTML.indexOf('<div id="chat-status-strip"'), indexHTML.indexOf('<div class="chat-composer-row">')).trimEnd();

async function measureStrip(page) {
  await page.setContent(`<style>${tokensCSS}
${chatCSS}</style>
    <div class="chat-page" style="width:900px">
      <header class="app-shell"></header><div class="chat-budget"></div><main class="chat-log"></main>
      <footer id="chat-composer" class="chat-composer">${stripMarkup}<div class="chat-composer-row"><textarea id="chat-input"></textarea></div></footer>
    </div>`);
  await page.evaluate(() => { document.getElementById("chat-notice").textContent = "idle"; });
  return page.evaluate(() => {
    const strip = document.getElementById("chat-status-strip");
    const notice = document.getElementById("chat-notice");
    const wrap = strip.querySelector(".chat-attach-wrap");
    const button = document.getElementById("chat-attach");
    const range = document.createRange();
    range.selectNodeContents(notice);
    const line = range.getBoundingClientRect();
    const box = strip.getBoundingClientRect();
    const b = button.getBoundingClientRect();
    const cx = b.left + b.width / 2;
    const cy = b.top + b.height / 2;
    const corners = [[-11, -11], [11, -11], [-11, 11], [11, 11]].map(([dx, dy]) => !!document.elementFromPoint(cx + dx, cy + dy)?.closest("#chat-attach"));
    const pad = Number.parseFloat(getComputedStyle(strip).paddingTop) + Number.parseFloat(getComputedStyle(strip).paddingBottom);
    return {
      height: box.height,
      textLine: notice.getBoundingClientRect().height,
      glyphInk: line.height,
      padding: pad,
      leftInset: notice.getBoundingClientRect().left - box.left,
      rightInset: box.right - wrap.getBoundingClientRect().right,
      textOffCentreBy: (line.top + line.bottom) / 2 - (box.top + box.bottom) / 2,
      corners,
    };
  });
}

test("the strip is its text plus its padding, inset the same at both ends, with the paperclip's target intact", async ({ page }) => {
  const m = await measureStrip(page);
  // (b): the height IS the text's line plus the padding, not a 24px control.
  expect(m.height).toBe(m.textLine + m.padding);
  expect(m.height).toBe(20);
  // (c): both ends inset the same.
  expect(m.rightInset).toBe(m.leftInset);
  expect(m.leftInset).toBe(4);
  // The hit target is NOT smaller: every corner of the old 24px square still
  // presses the paperclip, although its box is now 16px.
  expect(m.corners).toEqual([true, true, true, true]);
  // (d): measured, not nudged. The text was already on the centre line and this
  // records that rather than moving it.
  expect(Math.abs(m.textOffCentreBy)).toBeLessThanOrEqual(0.5);
});
