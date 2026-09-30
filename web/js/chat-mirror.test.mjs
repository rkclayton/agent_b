import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

const source = fs.readFileSync(new URL("./chat.js", import.meta.url), "utf8");

test("a phone-owned mirror is marked read-only with one Continue here control", () => {
  assert.match(source, /session\?\.origin === "phone" && session\?\.owner === "phone"/);
  assert.match(source, /input\.disabled = .*phoneOwned/);
  assert.match(source, /from phone · read-only while the phone owns this chat/);
  assert.equal((source.match(/textContent = "Continue here"/g) || []).length, 1);
  assert.match(source, /api\("\/api\/chat-mirror\/take", \{ chat_id: session\.id \}\)/);
});
