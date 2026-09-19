// Item 2ga (v1.0.0/W2): the release screenshot gate with masks. For every PNG
// in the baseline set, the candidate's PNG at the same relative path is
// compared pixel for pixel after the live-value rectangles recorded beside
// either image (<image>.masks.json, written by scripts/screenshot-masks.mjs)
// are blanked in both. A capture matches, matches as masked (the only
// differences lay inside masks, which are named), or differs.
//
//   node scripts/screenshot-gate.mjs BASELINE_DIR CANDIDATE_DIR [--report FILE]
import { readFile, readdir, writeFile } from "node:fs/promises";
import { join, relative } from "node:path";
import { inflateSync, deflateSync } from "node:zlib";
import { pathToFileURL } from "node:url";

// decodePNG reads the PNGs the browser writes: 8-bit, non-interlaced, RGB or
// RGBA. Anything else is refused rather than guessed at.
export function decodePNG(buffer) {
  if (buffer.readUInt32BE(0) !== 0x89504e47) throw new Error("not a PNG");
  let offset = 8, width = 0, height = 0, channels = 0;
  const data = [];
  while (offset < buffer.length) {
    const length = buffer.readUInt32BE(offset);
    const type = buffer.toString("latin1", offset + 4, offset + 8);
    const chunk = buffer.subarray(offset + 8, offset + 8 + length);
    if (type === "IHDR") {
      width = chunk.readUInt32BE(0); height = chunk.readUInt32BE(4);
      const [depth, color, , , interlace] = chunk.subarray(8);
      if (depth !== 8 || interlace !== 0 || (color !== 2 && color !== 6)) throw new Error(`unsupported PNG: depth ${depth}, colour type ${color}, interlace ${interlace}`);
      channels = color === 6 ? 4 : 3;
    } else if (type === "IDAT") data.push(chunk);
    else if (type === "IEND") break;
    offset += 12 + length;
  }
  const raw = inflateSync(Buffer.concat(data));
  const stride = width * channels, out = new Uint8Array(width * height * 4);
  let previous = new Uint8Array(stride);
  for (let y = 0; y < height; y++) {
    const filter = raw[y * (stride + 1)];
    const line = raw.subarray(y * (stride + 1) + 1, (y + 1) * (stride + 1));
    const row = new Uint8Array(stride);
    for (let i = 0; i < stride; i++) {
      const a = i >= channels ? row[i - channels] : 0, b = previous[i], c = i >= channels ? previous[i - channels] : 0;
      let p = 0;
      if (filter === 1) p = a;
      else if (filter === 2) p = b;
      else if (filter === 3) p = (a + b) >> 1;
      else if (filter === 4) { const e = a + b - c, pa = Math.abs(e - a), pb = Math.abs(e - b), pc = Math.abs(e - c); p = pa <= pb && pa <= pc ? a : pb <= pc ? b : c; }
      else if (filter !== 0) throw new Error(`bad PNG filter ${filter}`);
      row[i] = (line[i] + p) & 255;
    }
    for (let x = 0; x < width; x++) {
      for (let k = 0; k < 3; k++) out[(y * width + x) * 4 + k] = row[x * channels + k];
      out[(y * width + x) * 4 + 3] = channels === 4 ? row[x * channels + 3] : 255;
    }
    previous = row;
  }
  return { width, height, data: out };
}

// encodePNG writes RGBA, filter 0; the tests build their images with it.
export function encodePNG({ width, height, data }) {
  const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
  const crc = (bytes) => { let c = 0xffffffff; for (const byte of bytes) c = crcTable[(c ^ byte) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
  const chunk = (type, body) => {
    const head = Buffer.alloc(8); head.writeUInt32BE(body.length, 0); head.write(type, 4, "latin1");
    const tail = Buffer.alloc(4); tail.writeUInt32BE(crc(Buffer.concat([head.subarray(4), body])), 0);
    return Buffer.concat([head, body, tail]);
  };
  const header = Buffer.alloc(13); header.writeUInt32BE(width, 0); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = 6;
  const raw = Buffer.alloc(height * (width * 4 + 1));
  for (let y = 0; y < height; y++) Buffer.from(data.buffer, data.byteOffset + y * width * 4, width * 4).copy(raw, y * (width * 4 + 1) + 1);
  return Buffer.concat([Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]), chunk("IHDR", header), chunk("IDAT", deflateSync(raw)), chunk("IEND", Buffer.alloc(0))]);
}

// compareMasked counts differing pixels outside and inside the masks. masks
// is [{name, rects: [[x, y, w, h], …]}]; a rectangle past the image is clipped.
export function compareMasked(before, after, masks = []) {
  if (before.width !== after.width || before.height !== after.height) {
    return { dimensions: `${before.width}x${before.height} vs ${after.width}x${after.height}`, outside: before.width * before.height, inside: 0, bounds: null, masksHit: [] };
  }
  const { width, height } = before;
  const owner = new Int16Array(width * height).fill(-1);
  masks.forEach((mask, index) => {
    for (const [x, y, w, h] of mask.rects) {
      for (let row = Math.max(0, y); row < Math.min(height, y + h); row++) {
        for (let column = Math.max(0, x); column < Math.min(width, x + w); column++) owner[row * width + column] = index;
      }
    }
  });
  let outside = 0, inside = 0, bounds = null;
  const hit = new Set();
  for (let pixel = 0; pixel < width * height; pixel++) {
    const o = pixel * 4;
    if (before.data[o] === after.data[o] && before.data[o + 1] === after.data[o + 1] && before.data[o + 2] === after.data[o + 2] && before.data[o + 3] === after.data[o + 3]) continue;
    if (owner[pixel] >= 0) { inside++; hit.add(masks[owner[pixel]].name); continue; }
    outside++;
    const x = pixel % width, y = Math.floor(pixel / width);
    bounds = bounds ? { min_x: Math.min(bounds.min_x, x), min_y: Math.min(bounds.min_y, y), max_x: Math.max(bounds.max_x, x), max_y: Math.max(bounds.max_y, y) } : { min_x: x, min_y: y, max_x: x, max_y: y };
  }
  return { dimensions: `${width}x${height}`, outside, inside, bounds, masksHit: [...hit].sort() };
}

// mergeMasks is the union of two sidecars' masks, by name: a value that moved
// between runs is masked where it was in either.
export function mergeMasks(...sidecars) {
  const byName = new Map();
  for (const sidecar of sidecars) {
    for (const mask of sidecar?.masks ?? []) {
      const entry = byName.get(mask.name) ?? { name: mask.name, reason: mask.reason, rects: [] };
      entry.rects.push(...mask.rects);
      byName.set(mask.name, entry);
    }
  }
  return [...byName.values()];
}

// The gated set is every PNG under the baseline except a run-<clock> folder:
// chat-acceptance names its per-run evidence folder by the clock, so it has
// no counterpart in another run.
const pngs = async (dir) => (await Promise.all((await readdir(dir, { withFileTypes: true })).map((entry) => {
  const path = join(dir, entry.name);
  if (entry.isDirectory()) return entry.name.startsWith("run-") ? [] : pngs(path);
  return entry.name.endsWith(".png") ? [path] : [];
}))).flat();

const sidecar = async (path) => {
  try { return JSON.parse(await readFile(`${path}.masks.json`, "utf8")); } catch (error) { if (error.code === "ENOENT") return null; throw error; }
};

// gate compares every baseline capture with the candidate's.
export async function gate(baselineDir, candidateDir) {
  const results = [];
  for (const path of (await pngs(baselineDir)).sort()) {
    const name = relative(baselineDir, path).replaceAll("\\", "/");
    let candidate;
    try { candidate = await readFile(join(candidateDir, name)); } catch { results.push({ name, verdict: "missing" }); continue; }
    const [baseMasks, candidateMasks] = await Promise.all([sidecar(path), sidecar(join(candidateDir, name))]);
    const masks = mergeMasks(baseMasks, candidateMasks);
    const outcome = compareMasked(decodePNG(await readFile(path)), decodePNG(candidate), masks);
    const verdict = outcome.outside ? "differs" : outcome.inside ? "masked" : "match";
    results.push({ name, verdict, ...outcome, masks: masks.map((mask) => `${mask.name}×${mask.rects.length}`), sidecars: { baseline: Boolean(baseMasks), candidate: Boolean(candidateMasks) } });
  }
  return results;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const args = process.argv.slice(2);
  const reportAt = args.indexOf("--report");
  const report = reportAt >= 0 ? args.splice(reportAt, 2)[1] : null;
  if (args.length !== 2) { console.error("usage: node scripts/screenshot-gate.mjs BASELINE_DIR CANDIDATE_DIR [--report FILE]"); process.exit(2); }
  const results = await gate(args[0], args[1]);
  for (const r of results) {
    if (r.verdict === "missing") console.log(`MISSING ${r.name}`);
    else if (r.verdict === "differs") console.log(`DIFFERS ${r.name} ${r.outside} px outside masks ${JSON.stringify(r.bounds)}${r.inside ? `; ${r.inside} px inside (${r.masksHit.join(", ")})` : ""}`);
    else if (r.verdict === "masked") console.log(`MATCH   ${r.name} (masked: ${r.masksHit.join(", ")}; ${r.inside} px inside masks)`);
    else console.log(`MATCH   ${r.name}`);
  }
  const passed = results.filter((r) => r.verdict === "match" || r.verdict === "masked").length;
  const pass = passed === results.length && results.length > 0;
  console.log(`SCREENSHOT GATE ${pass ? "PASS" : "FAIL"} ${passed} of ${results.length} match${results.length ? "" : " (the baseline set is empty)"}`);
  if (report) await writeFile(report, `${JSON.stringify(results, null, 1)}\n`);
  process.exit(pass ? 0 : 1);
}
