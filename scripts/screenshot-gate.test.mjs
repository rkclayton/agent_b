import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { compareMasked, decodePNG, encodePNG, gate, maskedArea, mergeMasks, trustedMasks } from "./screenshot-gate.mjs";

const image = (width, height, paint = () => [20, 22, 26, 255]) => {
  const data = new Uint8Array(width * height * 4);
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) data.set(paint(x, y), (y * width + x) * 4);
  return { width, height, data };
};
const withPixel = (source, x, y, rgba) => {
  const copy = { ...source, data: source.data.slice() };
  copy.data.set(rgba, (y * source.width + x) * 4);
  return copy;
};
const masks = [{ name: "duration", reason: "measured", rects: [[10, 10, 8, 4]] }];

test("a PNG round-trips through the gate's decoder", () => {
  const source = image(17, 9, (x, y) => [x * 13, y * 29, (x + y) * 7, 255]);
  assert.deepEqual(decodePNG(encodePNG(source)), source);
});

test("an unchanged capture matches", () => {
  const base = image(40, 30);
  assert.deepEqual(compareMasked(base, image(40, 30), masks), { dimensions: "40x30", outside: 0, inside: 0, rounding: 0, bounds: null, masksHit: [] });
});

test("a one-pixel change outside every mask fails", () => {
  const base = image(40, 30);
  const result = compareMasked(base, withPixel(base, 3, 3, [21, 22, 26, 255]), masks);
  assert.equal(result.outside, 1);
  assert.deepEqual(result.bounds, { min_x: 3, min_y: 3, max_x: 3, max_y: 3 });
});

test("a one-pixel change inside a mask passes and names the mask", () => {
  const base = image(40, 30);
  const result = compareMasked(base, withPixel(base, 12, 11, [255, 0, 0, 255]), masks);
  assert.equal(result.outside, 0);
  assert.equal(result.inside, 1);
  assert.deepEqual(result.masksHit, ["duration"]);
});

test("the mask edge is exact: the pixel just past it is outside", () => {
  const base = image(40, 30);
  assert.equal(compareMasked(base, withPixel(base, 18, 11, [255, 0, 0, 255]), masks).outside, 1);
  assert.equal(compareMasked(base, withPixel(base, 17, 13, [255, 0, 0, 255]), masks).outside, 0);
  assert.equal(compareMasked(base, withPixel(base, 17, 14, [255, 0, 0, 255]), masks).outside, 1);
});

test("different dimensions never match", () => {
  assert.ok(compareMasked(image(40, 30), image(40, 31), masks).outside > 0);
});

test("masks from both sidecars are merged by name", () => {
  const merged = mergeMasks({ masks: [{ name: "duration", reason: "r", rects: [[0, 0, 1, 1]] }] }, { masks: [{ name: "duration", reason: "r", rects: [[5, 5, 1, 1]] }, { name: "timestamp", reason: "t", rects: [[9, 9, 1, 1]] }] }, null);
  assert.deepEqual(merged.map((mask) => [mask.name, mask.rects.length]), [["duration", 2], ["timestamp", 1]]);
});

test("the gate reads sidecars beside each image and reports match, masked, differs and missing", async () => {
  const root = await mkdtemp(join(tmpdir(), "screenshot-gate-"));
  const [baseline, candidate] = [join(root, "baseline"), join(root, "candidate")];
  await Promise.all([mkdir(join(baseline, "sub"), { recursive: true }), mkdir(join(candidate, "sub"), { recursive: true })]);
  const base = image(40, 30);
  const sidecar = `${JSON.stringify({ element: false, masks })}\n`;
  for (const name of ["same.png", "sub/live.png", "outside.png", "gone.png"]) {
    await writeFile(join(baseline, name), encodePNG(base));
    await writeFile(join(baseline, `${name}.masks.json`), sidecar);
  }
  await writeFile(join(candidate, "same.png"), encodePNG(base));
  await writeFile(join(candidate, "sub/live.png"), encodePNG(withPixel(base, 12, 11, [255, 0, 0, 255])));
  await writeFile(join(candidate, "outside.png"), encodePNG(withPixel(base, 30, 25, [255, 0, 0, 255])));
  const results = Object.fromEntries((await gate(baseline, candidate)).map((r) => [r.name, r.verdict]));
  assert.deepEqual(results, { "gone.png": "missing", "outside.png": "differs", "same.png": "match", "sub/live.png": "masked" });
});

test("a candidate cannot mask a region its baseline did not declare", async () => {
  const root = await mkdtemp(join(tmpdir(), "screenshot-gate-trust-"));
  const [baseline, candidate] = [join(root, "baseline"), join(root, "candidate")];
  await Promise.all([mkdir(baseline), mkdir(candidate)]);
  const base = image(40, 30);
  await writeFile(join(baseline, "a.png"), encodePNG(base));
  await writeFile(join(baseline, "a.png.masks.json"), JSON.stringify({ masks }));
  // The candidate changes (30, 25) and declares a mask of its own there.
  await writeFile(join(candidate, "a.png"), encodePNG(withPixel(base, 30, 25, [255, 0, 0, 255])));
  await writeFile(join(candidate, "a.png.masks.json"), JSON.stringify({ masks: [{ name: "duration", rects: [[28, 23, 6, 6]] }, { name: "invented", rects: [[0, 0, 40, 30]] }] }));
  const [result] = await gate(baseline, candidate);
  assert.equal(result.verdict, "differs");
});

test("a candidate's rectangle that overlaps the baseline's same mask is honoured", () => {
  const trusted = trustedMasks({ masks: [{ name: "duration", rects: [[10, 10, 8, 4]] }] }, { masks: [{ name: "duration", rects: [[14, 10, 10, 4], [30, 20, 4, 4]] }, { name: "timestamp", rects: [[10, 10, 8, 4]] }] });
  assert.deepEqual(trusted, [{ name: "duration", reason: undefined, rects: [[10, 10, 8, 4], [14, 10, 10, 4]] }]);
  assert.equal(maskedArea(trusted, 40, 30), 56 / 1200);
});

test("the gate counts a one-level difference as rounding and fails a two-level one", () => {
  const base = image(40, 30);
  const one = compareMasked(base, withPixel(base, 3, 3, [21, 22, 26, 255]), masks, { tolerance: 1 });
  assert.deepEqual([one.outside, one.rounding], [0, 1]);
  const two = compareMasked(base, withPixel(base, 3, 3, [22, 22, 26, 255]), masks, { tolerance: 1 });
  assert.deepEqual([two.outside, two.rounding], [1, 0]);
});
