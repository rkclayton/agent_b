import { fileURLToPath } from "node:url";
import { expect, test } from "@playwright/test";

// The module imports its siblings, so the real web/ tree is served rather than
// inlined: the gate then exercises the module exactly as the browser loads it.
const webRoot = fileURLToPath(new URL("../../web/", import.meta.url));

// Item 2lc. The operator, 2026-09-26: "copy paste out of the chat window is
// terrible … when i copy a small bit of text it takes more of the window then i
// want. it should be exact and if you encompass collapsed carrots just not copy
// it." These are exact-string fixtures against a real selection in a real DOM.
const TRANSCRIPT = `
  <div id="chat-log">
    <section class="chat-entry chat-user"><div id="u1">make the thing smaller</div></section>
    <section class="chat-entry chat-agent chat-response">
      <div id="p1">Here is the first paragraph of the answer.</div>
      <button class="tool-tick" id="tick1">tool list_dir . → ok 0 ms</button>
      <div class="chat-step-summary" id="sum1">steps · 2 tool calls · 2.3 s</div>
      <button class="tool-tick" id="tick2">thought 2.3 s (~74 tokens)</button>
      <div id="p2">And the closing paragraph.</div>
    </section>
  </div>`;

async function copyBetween(page, startId, startOffset, endId, endOffset) {
  return page.evaluate(async ({ startId, startOffset, endId, endOffset }) => {
    const module = await import("/js/transcript-copy.js");
    const range = document.createRange();
    const first = document.getElementById(startId).firstChild;
    const last = document.getElementById(endId).firstChild;
    range.setStart(first, startOffset);
    range.setEnd(last, endOffset);
    const selection = window.getSelection();
    selection.removeAllRanges();
    selection.addRange(range);
    return module.transcriptText(module.rowsInSelection(document.getElementById("chat-log"), selection));
  }, { startId, startOffset, endId, endOffset });
}

test.beforeEach(async ({ page }) => {
  await page.route("**/*", (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/fixture.html") return route.fulfill({ contentType: "text/html", body: `<!doctype html><html><body>${TRANSCRIPT}</body></html>` });
    return route.fulfill({ path: webRoot + path.replace(/^\//, "") });
  });
  await page.goto("http://transcript.test/fixture.html");
});

// (a) and (c): a few words inside one agent turn copy those words, with no mark.
test("a few words inside one turn copy exactly those words", async ({ page }) => {
  expect(await copyBetween(page, "p1", 12, "p1", 29)).toBe("first paragraph o");
});

// (c): a selection spanning a user line and the following agent prose copies both
// with their two marks and one blank line between.
test("a selection across speakers marks each speaker once", async ({ page }) => {
  expect(await copyBetween(page, "u1", 0, "p1", 8)).toBe("you: make the thing smaller\n\nagent_b: Here is");
});

// (b): a selection crossing the collapsed step rows copies the prose either side
// and none of the step rows.
test("collapsed step and fold rows are not copied", async ({ page }) => {
  const text = await copyBetween(page, "p1", 0, "p2", 26);
  expect(text).toBe("Here is the first paragraph of the answer.\nAnd the closing paragraph.");
  for (const furniture of ["steps ·", "tool list_dir", "thought 2.3"]) {
    expect(text, `${furniture} was copied`).not.toContain(furniture);
  }
});

// (c) and (e): a selection covering a complete turn copies that whole turn, and
// because it lies within ONE entry there is no change of speaker to mark. The
// anti-spoofing guarantee — an agent turn reading "you: …" cannot paste as the
// operator — lives where the mark is decided, and transcript-copy.test.mjs pins it.
test("a whole-turn selection copies the whole turn", async ({ page }) => {
  expect(await copyBetween(page, "u1", 0, "u1", 22)).toBe("make the thing smaller");
});

// (d): normalization is light, and never rewrites the selected words.
test("normalization collapses blank runs and trims, nothing else", async ({ page }) => {
  const normalized = await page.evaluate(async () => {
    const module = await import("/js/transcript-copy.js");
    return module.normalize("  keep   inner  spacing \r\n\n\n\nand this  \n");
  });
  expect(normalized).toBe("keep   inner  spacing\n\nand this");
});
