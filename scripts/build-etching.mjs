// Item 2cz (v1.0.0/W3): the whole-window etching map, built from the
// operator's circuit image. The pipeline, every parameter below:
//
//   1. grayscale   Rec. 601 luma in integers.
//   2. expand      mirrored tiling to one repeating period of twice the source
//                  in each direction. The lower half is the mirrored rows
//                  shifted sideways, and each horizontal seam is cut along the
//                  path where the two halves differ least (a minimum-error
//                  boundary cut), so the pattern continues rather than
//                  reflecting on a straight line. The vertical mirror axes are
//                  exact continuations.
//   3. emboss      one light direction over the whole period, wrapping at its
//                  edges, so the map tiles without a seam. (Expanding before
//                  embossing keeps the light from the top left in every tile;
//                  embossing first would light mirrored tiles from below.)
//   4. normalise   the emboss scaled so its 99.5th percentile magnitude is one
//                  step, clamped, then a few percent around mid-grey:
//                  128 ± STEP.
//   5. the map     that mid-grey emboss in the form the overlay draws: each
//                  pixel white or black at alpha 2·|g − 128|, which over any
//                  surface is the same as hard-light blending the mid-grey
//                  emboss onto it. So each surface shows its own tone
//                  lightened on one edge and darkened on the other; no colour
//                  is added. The fade token (--etch-fade in tokens.css) scales it.
//
//   node scripts/build-etching.mjs [--check]
// --check rebuilds in memory and fails if the checked-in map differs.
import { createHash } from "node:crypto";
import { readFileSync, writeFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
import { decodePNG, encodePNG } from "./png.mjs";

export const PARAMETERS = Object.freeze({
  source: "web/assets/source/circuit-source.png",
  sourceSHA256: "bc721195fe00e93ac470d7c7117f05a89e4cb6931b4a7aed05af530c180f6088",
  output: "web/assets/etching.png",
  luma: [299, 587, 114],
  emboss: [[-1, -1, 0], [-1, 0, 1], [0, 1, 1]],
  percentile: 0.995,
  step: 15,
  rowShift: 0.382,
  seamBand: 160,
});

export function grayscale({ width, height, data }, [r, g, b] = PARAMETERS.luma) {
  const out = new Uint8Array(width * height);
  for (let i = 0; i < out.length; i++) out[i] = Math.round((r * data[i * 4] + g * data[i * 4 + 1] + b * data[i * 4 + 2]) / 1000);
  return { width, height, data: out };
}

// mirror folds any coordinate into [0, n) by mirrored tiling with period 2n.
const mirror = (v, n) => { const m = ((v % (2 * n)) + 2 * n) % (2 * n); return m < n ? m : 2 * n - 1 - m; };

// seamCut is the row, per column, where the upper field gives way to the lower
// one inside [top, top + band): the cheapest path by |upper − lower| that moves
// at most a row a column and ends within a row of where it started, so the
// period still repeats sideways. Ties go to the lower index.
export function seamCut(width, top, band, upper, lower) {
  const cost = (x, r) => Math.abs(upper(x, top + r) - lower(x, top + r));
  let best = null;
  for (let start = 0; start < band; start++) {
    let previous = new Float64Array(band).fill(Infinity);
    previous[start] = cost(0, start);
    const from = [];
    for (let x = 1; x < width; x++) {
      const current = new Float64Array(band).fill(Infinity), back = new Int16Array(band);
      for (let r = 0; r < band; r++) {
        let pick = -1, value = Infinity;
        for (const d of [0, -1, 1]) { const q = r + d; if (q >= 0 && q < band && previous[q] < value) { value = previous[q]; pick = q; } }
        if (pick >= 0) { current[r] = value + cost(x, r); back[r] = pick; }
      }
      from.push(back);
      previous = current;
    }
    let end = -1, total = Infinity;
    for (const r of [start, start - 1, start + 1]) if (r >= 0 && r < band && previous[r] < total) { total = previous[r]; end = r; }
    if (end >= 0 && (!best || total < best.total)) {
      const rows = new Int16Array(width);
      rows[width - 1] = end;
      for (let x = width - 1; x > 0; x--) rows[x - 1] = from[x - 1][rows[x]];
      best = { total, rows: Array.from(rows, (r) => top + r) };
    }
  }
  return best.rows;
}

// expand builds one period, 2W × 2H, of the mirrored tiling with dissolved
// horizontal seams (step 2).
export function expand({ width: w, height: h, data }, { rowShift = PARAMETERS.rowShift, seamBand = PARAMETERS.seamBand } = {}) {
  const W = 2 * w, H = 2 * h, shift = Math.round(w * rowShift), half = seamBand >> 1;
  const upper = (x, y) => data[mirror(y, h) * w + mirror(x, w)];
  const lower = (x, y) => data[mirror(y, h) * w + mirror(x + shift, w)];
  // The seam into the lower half near y = h, and back into the next period's
  // upper half near y = 0 (seen as rows -half … half).
  const down = seamCut(W, h - half, seamBand, upper, lower);
  const up = seamCut(W, -half, seamBand, lower, upper);
  const out = new Uint8Array(W * H);
  for (let x = 0; x < W; x++) {
    for (let y = 0; y < H; y++) {
      const t = y >= H - half ? y - H : y; // rows near the wrap, as negatives
      const fromUpper = t >= up[x] && t < down[x];
      out[y * W + x] = fromUpper ? upper(x, y) : lower(x, y);
    }
  }
  return { width: W, height: H, data: out, seams: { down, up } };
}

// emboss applies the kernel, wrapping at the edges (step 3).
export function emboss({ width, height, data }, kernel = PARAMETERS.emboss) {
  const out = new Int16Array(width * height);
  for (let y = 0; y < height; y++) {
    for (let x = 0; x < width; x++) {
      let sum = 0;
      for (let j = 0; j < 3; j++) for (let i = 0; i < 3; i++) {
        const k = kernel[j][i];
        if (k) sum += k * data[((y + j - 1 + height) % height) * width + ((x + i - 1 + width) % width)];
      }
      out[y * width + x] = sum;
    }
  }
  return { width, height, data: out };
}

// normalise maps the emboss to 128 ± step (step 4).
export function normalise({ width, height, data }, { percentile = PARAMETERS.percentile, step = PARAMETERS.step } = {}) {
  const magnitudes = Float64Array.from(data.filter((v) => v !== 0), Math.abs).sort();
  const scale = magnitudes.length ? magnitudes[Math.min(magnitudes.length - 1, Math.floor(magnitudes.length * percentile))] : 1;
  const out = new Uint8Array(width * height);
  for (let i = 0; i < out.length; i++) out[i] = 128 + Math.round(step * Math.max(-1, Math.min(1, data[i] / scale)));
  return { width, height, data: out, scale };
}

// overlayMap is the mid-grey emboss as white/black over alpha (step 5).
export function overlayMap({ width, height, data }) {
  const out = new Uint8Array(width * height * 2);
  for (let i = 0; i < width * height; i++) { const m = data[i] - 128; out[i * 2] = m > 0 ? 255 : 0; out[i * 2 + 1] = 2 * Math.abs(m); }
  return { width, height, data: out, grey: true };
}

export function buildEtching(sourceBytes, parameters = PARAMETERS) {
  const grey = grayscale(decodePNG(sourceBytes), parameters.luma);
  const period = expand(grey, parameters);
  const map = normalise(emboss(period, parameters.emboss), parameters);
  return { png: encodePNG(overlayMap(map)), width: map.width, height: map.height, scale: map.scale, seams: period.seams };
}

const sha256 = (bytes) => createHash("sha256").update(bytes).digest("hex");

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const source = readFileSync(PARAMETERS.source);
  if (sha256(source) !== PARAMETERS.sourceSHA256) {
    console.error(`${PARAMETERS.source} is not the operator's image this map was built from (sha256 ${sha256(source)}, expected ${PARAMETERS.sourceSHA256})`);
    process.exit(1);
  }
  const built = buildEtching(source);
  if (process.argv.includes("--check")) {
    const same = sha256(readFileSync(PARAMETERS.output)) === sha256(built.png);
    console.log(`${PARAMETERS.output} ${same ? "matches" : "DIFFERS FROM"} a rebuild from the source`);
    process.exit(same ? 0 : 1);
  }
  writeFileSync(PARAMETERS.output, built.png);
  console.log(`${PARAMETERS.output}: ${built.width}×${built.height}, ${built.png.length} bytes, sha256 ${sha256(built.png)}, emboss scale ${built.scale}`);
}
