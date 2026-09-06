import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const css = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");

test("Chat copies original assistant markdown and documents composer keys", () => {
  assert.match(chat, /navigator\.clipboard\?\.writeText\(assistantCopyText\(item, entry\.items\)\)/);
  assert.match(chat, /expanded\.has\(entry\.key\) && entry\.reasoning/);
  assert.match(chat, /expanded\.has\(item\.key\)/);
  assert.match(chat, /Copy message/);
  assert.match(chat, /Enter sends · Shift\+Enter newline/);
});

test("Chat selection excludes chrome while preserving message content", () => {
  for (const selector of [".chat-speaker", ".thinking-line", ".tool-tick", ".chat-notice-row", ".chat-jump"]) {
    const at = css.indexOf(selector);
    assert.notEqual(at, -1, `${selector} missing`);
    assert.match(css.slice(at, at + 500), /user-select:\s*none/);
  }
  const content = css.slice(css.indexOf(".chat-content {"), css.indexOf(".chat-content {") + 200);
  assert.match(content, /user-select:\s*text/);
});
