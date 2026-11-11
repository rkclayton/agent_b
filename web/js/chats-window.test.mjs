import test from "node:test";
import assert from "node:assert/strict";
import { filterChats, virtualRange } from "./chats-window.js";

test("the Chats window filters names immediately and case-insensitively 2xs", () => {
  const rows = [{ name: "Alpha budget" }, { name: "beta ALPHABET" }, { name: "Gamma" }];
  assert.deepEqual(filterChats(rows, "alp").map(({ name }) => name), ["Alpha budget", "beta ALPHABET"]);
  assert.equal(filterChats(rows, "none").length, 0);
  assert.equal(filterChats(rows, "").length, 3);
});

test("two thousand chats render only a visible window plus margin 2xs", () => {
  assert.deepEqual(virtualRange(0, 360, 2000, 28, 6), { start: 0, end: 19 });
  const middle = virtualRange(28000, 360, 2000, 28, 6);
  assert.ok(middle.start > 900 && middle.end - middle.start <= 26);
  assert.deepEqual(virtualRange(55999, 360, 2000, 28, 6), { start: 1981, end: 2000 });
});
