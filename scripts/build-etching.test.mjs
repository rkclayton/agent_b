import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, readFileSync } from "node:fs";
import test from "node:test";
import { PARAMETERS, buildEtching, emboss, expand, grayscale, seamCut } from "./build-etching.mjs";
import { decodePNG, encodePNG } from "./png.mjs";

// A small line drawing: light traces on a blue field, like the source.
const source = (() => {
  const width = 48, height = 20, data = new Uint8Array(width * height * 4);
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) {
    const trace = y === 5 || x === 30 || (y === 14 && x > 8 && x < 40) || (x - y === 10);
    data.set(trace ? [150, 170, 230, 255] : [46, 62, 180, 255], (y * width + x) * 4);
  }
  return encodePNG({ width, height, data });
})();

test("two builds from one source are byte-identical", () => {
  assert.equal(Buffer.compare(buildEtching(source).png, buildEtching(source).png), 0);
});

test("the map is one period twice the source each way, white or black at no more than twice the step", () => {
  const built = buildEtching(source);
  const map = decodePNG(built.png);
  assert.deepEqual([map.width, map.height], [96, 40]);
  let lit = 0;
  for (let i = 0; i < map.width * map.height; i++) {
    const [r, g, b, a] = map.data.subarray(i * 4, i * 4 + 4);
    assert.ok(r === g && g === b && (r === 0 || r === 255), `pixel ${i} is not white or black`);
    assert.ok(a <= 2 * PARAMETERS.step, `pixel ${i} alpha ${a}`);
    if (a) lit++;
  }
  assert.ok(lit > 0, "the emboss drew nothing");
});

test("a flat source embosses to nothing: no edge, no texture", () => {
  const data = new Uint8Array(16 * 8 * 4).fill(90);
  const map = decodePNG(buildEtching(encodePNG({ width: 16, height: 8, data })).png);
  for (let i = 0; i < map.width * map.height; i++) assert.equal(map.data[i * 4 + 3], 0);
});

test("the emboss wraps, so the period repeats without an edge", () => {
  const period = expand(grayscale(decodePNG(source)), { rowShift: PARAMETERS.rowShift, seamBand: 8 });
  const e = emboss(period);
  // Shift the period by a third each way: the emboss of the shifted field is
  // the shifted emboss, which holds only when the edges wrap.
  const { width: w, height: h } = period;
  const shifted = { width: w, height: h, data: period.data.map((_, i) => period.data[(((i / w | 0) + 13) % h) * w + ((i % w) + 29) % w]) };
  const es = emboss(shifted);
  for (let i = 0; i < w * h; i++) assert.equal(es.data[i], e.data[(((i / w | 0) + 13) % h) * w + ((i % w) + 29) % w]);
});

test("the seam cut follows where the two fields agree and closes on itself", () => {
  // The fields differ everywhere except row 3 of the band.
  const rows = seamCut(20, 10, 6, (x, y) => (y === 13 ? 7 : 0), () => 7);
  assert.ok(rows.every((r) => r === 13), rows.join(","));
  const wander = seamCut(30, 0, 8, (x, y) => (y === (x < 15 ? 2 : 5) ? 1 : 0), () => 1);
  assert.ok(Math.abs(wander[0] - wander.at(-1)) <= 1);
  for (let x = 1; x < wander.length; x++) assert.ok(Math.abs(wander[x] - wander[x - 1]) <= 1);
});

test("the checked-in map is the rebuild of the operator's source", { skip: !existsSync(PARAMETERS.source) && "the operator's source image is not in this tree" }, () => {
  const bytes = readFileSync(PARAMETERS.source);
  assert.equal(createHash("sha256").update(bytes).digest("hex"), PARAMETERS.sourceSHA256);
  assert.equal(Buffer.compare(buildEtching(bytes).png, readFileSync(PARAMETERS.output)), 0);
});
