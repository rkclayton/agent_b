import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

const chatCSS = await readFile(new URL("../../web/css/chat.css", import.meta.url), "utf8");

const tokensCSS = await readFile(new URL("../../web/css/tokens.css", import.meta.url), "utf8");

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
