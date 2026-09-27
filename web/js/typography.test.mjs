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

test("the default reading size is named once, and a chosen size still wins", () => {
  // (f): whatever the default is, it is the value used only when the setting is
  // UNSET, so a reader who chose a size keeps it. (d) tried "large" and
  // rel-1.23.0/W8 put it back: at that step the transcript overflowed a 320px
  // viewport by two pixels and the chat acceptance gate said so.
  assert.match(chatJS, /const DEFAULT_TEXT_SIZE = "(?:small|normal|large|larger|largest)"/);
  assert.match(chatJS, /TEXT_SCALES\[chat\.text_size\] \?\? TEXT_SCALES\[DEFAULT_TEXT_SIZE\]/);
  // The fallback fires only for an unset value — "small" must still be 0.9.
  const scales = /const TEXT_SCALES = \{([^}]*)\}/.exec(chatJS)[1];
  assert.match(scales, /small:\s*0\.9/);
  assert.match(scales, /large:\s*1\.15/);
});

test("the shipped faces are still the ones the list offers", () => {
  // (e): the face joins the list rather than replacing it, and nothing is
  // marked as an accessibility option.
  const files = fs.readdirSync(new URL("../assets/fonts", import.meta.url));
  for (const face of ["ibm-plex-sans", "ibm-plex-mono", "atkinson-hyperlegible", "opendyslexic"]) {
    assert.ok(files.some((name) => name.startsWith(face)), `${face} is no longer shipped`);
  }
});
