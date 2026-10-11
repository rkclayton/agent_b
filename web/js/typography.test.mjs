import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2m7: the operator's two reading complaints, asserted rather than
// eyeballed — which is the item's own word for (c).
const read = (relative) => fs.readFileSync(new URL(relative, import.meta.url), "utf8");
const tokens = read("../css/tokens.css");
const chatCSS = read("../css/chat.css");
const chatJS = read("./chat.js");

test("the background texture is half what it was", () => {
  // (a): the value before this item was .165, and the report states it so the
  // change is checkable rather than asserted.
  const match = /--etch-fade:\s*([0-9.]+)/.exec(tokens);
  assert.ok(match, "the etch dial is gone");
  assert.equal(Number(match[1]), 0.0825);
});

test("no bold is synthesized: every weight asked for is one the product ships", () => {
  // (c). Markdown emits <strong>, nothing set its weight, and the browser's
  // default is 700 — a weight this product does not ship, so every bold word in
  // every answer was the 400 face smeared wider by the rasteriser.
  const shipped = new Set(["400", "500", "normal"]);
  const asked = [...`${tokens}\n${chatCSS}\n${read("../css/app.css")}`.matchAll(/font-weight:\s*([a-z0-9]+)/g)].map((m) => m[1]);
  const unshipped = asked.filter((weight) => !shipped.has(weight));
  assert.deepEqual(unshipped, [], `these weights are not shipped as files: ${unshipped.join(", ")}`);
  assert.match(chatCSS, /\.chat-response-answer strong/, "prose bold has no weight of its own, so the browser picks 700");
});

test("the reading size is a bounded whole-pixel value with the old steps mapped once", () => {
  assert.match(chatJS, /const DEFAULT_TEXT_SIZE = 12/);
  assert.match(chatJS, /Number\.isInteger\(chat\.text_size\).*Math\.min\(32, Math\.max\(9, pixels\)\)/s);
  const migration = /const NAMED_PIXELS = \{([^}]*)\}/.exec(chatJS)?.[1] || "";
  for (const pair of [/small:\s*11/, /normal:\s*12/, /large:\s*14/, /larger:\s*16/, /largest:\s*18/]) {
    assert.match(migration, pair);
  }
  assert.match(chatJS, /const boundedPixels = Math\.min\(32, Math\.max\(9, pixels\)\)/);
  assert.match(chatJS, /setProperty\("--chat-size", `\$\{boundedPixels\}px`\)/);
});

test("the shipped faces are still the ones the list offers", () => {
  // (e): the face joins the list rather than replacing it, and nothing is
  // marked as an accessibility option.
  const files = fs.readdirSync(new URL("../assets/fonts", import.meta.url));
  for (const face of ["ibm-plex-sans", "ibm-plex-mono", "atkinson-hyperlegible", "opendyslexic"]) {
    assert.ok(files.some((name) => name.startsWith(face)), `${face} is no longer shipped`);
  }
});
