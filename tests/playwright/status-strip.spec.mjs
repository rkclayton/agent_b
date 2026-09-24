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
  const hidden = await measure(page, false);
  expect(hidden.attachRightGap).toBeLessThanOrEqual(1);

  const visible = await measure(page, true);
  expect(visible.attachRightGap).toBeLessThanOrEqual(1);
  expect(visible.bannerAttachGap).toBeGreaterThanOrEqual(5);
  expect(visible.bannerAttachGap).toBeLessThanOrEqual(7);
});
