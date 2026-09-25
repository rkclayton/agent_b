// Item 2ga (v1.0.0/W2): the release screenshot gate with masks. For every PNG
// in the baseline set, the candidate's PNG at the same relative path is
// compared pixel for pixel after the live-value rectangles recorded beside
// the baseline image (<image>.masks.json, written by tests/screenshot-masks.mjs)
// are blanked in both; a candidate's own rectangles count only where they
// overlap a baseline one of the same name. A capture matches, matches as masked
// (the only differences lay inside masks, which are named), or is unexplained.
// Any unexplained pixel blocks the gate.
//
//   node tests/screenshot-gate.mjs BASELINE_DIR CANDIDATE_DIR [--report FILE]
import { readFile, readdir, writeFile } from "node:fs/promises";
import { join, relative } from "node:path";
import { decodePNG, encodePNG } from "./png.mjs";
import { pathToFileURL } from "node:url";

export { decodePNG, encodePNG };

// compareMasked counts differing pixels outside and inside the masks. masks
// is [{name, rects: [[x, y, w, h], …]}]; a rectangle past the image is clipped.
// tolerance is the largest channel difference counted as equal: 0 compares
// exactly; the gate passes 2, since two runs of one build can round a channel
// by two levels where the fixed etching compositor crosses a one-pixel border
// (item 2jh reproduced this at exactly x300/y147 and x301/y148). A pixel
// within it counts as rounding and is reported, never masked; any ordinary
// visible mutation remains unexplained and blocks the gate.
export function compareMasked(before, after, masks = [], { tolerance = 0 } = {}) {
  if (before.width !== after.width || before.height !== after.height) {
    return { dimensions: `${before.width}x${before.height} vs ${after.width}x${after.height}`, outside: before.width * before.height, inside: 0, rounding: 0, bounds: null, masksHit: [] };
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
  let outside = 0, inside = 0, rounding = 0, bounds = null;
  const hit = new Set();
  for (let pixel = 0; pixel < width * height; pixel++) {
    const o = pixel * 4;
    if (before.data[o] === after.data[o] && before.data[o + 1] === after.data[o + 1] && before.data[o + 2] === after.data[o + 2] && before.data[o + 3] === after.data[o + 3]) continue;
    if (tolerance && Math.max(...[0, 1, 2, 3].map((k) => Math.abs(before.data[o + k] - after.data[o + k]))) <= tolerance) { rounding++; continue; }
    if (owner[pixel] >= 0) { inside++; hit.add(masks[owner[pixel]].name); continue; }
    outside++;
    const x = pixel % width, y = Math.floor(pixel / width);
    bounds = bounds ? { min_x: Math.min(bounds.min_x, x), min_y: Math.min(bounds.min_y, y), max_x: Math.max(bounds.max_x, x), max_y: Math.max(bounds.max_y, y) } : { min_x: x, min_y: y, max_x: x, max_y: y };
  }
  return { dimensions: `${width}x${height}`, outside, inside, rounding, bounds, masksHit: [...hit].sort() };
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

// trustedMasks is what the gate blanks: the baseline's masks, and a
// candidate rectangle only where it overlaps a baseline rectangle of the same
// name (the same live value, moved or widened). A candidate cannot mask a
// region the baseline did not declare (v1.0.0/W4 cold review: the candidate
// writes its own sidecar).
export function trustedMasks(baseline, candidate) {
  const overlaps = ([ax, ay, aw, ah], [bx, by, bw, bh]) => ax < bx + bw && bx < ax + aw && ay < by + bh && by < ay + ah;
  const byName = new Map((baseline?.masks ?? []).map((mask) => [mask.name, mask.rects]));
  const extra = { masks: (candidate?.masks ?? []).map((mask) => ({ ...mask, rects: mask.rects.filter((rect) => (byName.get(mask.name) ?? []).some((base) => overlaps(rect, base))) })) };
  return mergeMasks(baseline, extra).filter((mask) => mask.rects.length);
}

// maskedArea is the share of an image the masks cover.
export function maskedArea(masks, width, height) {
  const covered = new Uint8Array(width * height);
  for (const { rects } of masks) for (const [x, y, w, h] of rects) for (let r = Math.max(0, y); r < Math.min(height, y + h); r++) covered.fill(1, r * width + Math.max(0, x), r * width + Math.min(width, x + w));
  return covered.reduce((sum, v) => sum + v, 0) / (width * height);
}

const sidecar = async (path) => {
  try { return JSON.parse(await readFile(`${path}.masks.json`, "utf8")); } catch (error) { if (error.code === "ENOENT") return null; throw error; }
};

// gate compares every baseline capture with the candidate's.
export async function gate(baselineDir, candidateDir, { tolerance = 2 } = {}) {
  const results = [];
  const paths = (await pngs(baselineDir)).sort();
  const sidecars = [];
  for (const path of paths) {
    const name = relative(baselineDir, path).replaceAll("\\", "/");
    sidecars.push(...await Promise.all([sidecar(path), sidecar(join(candidateDir, name))]));
  }
  const present = new Set(sidecars.flatMap((value) => (value?.masks ?? []).filter((mask) => mask.rects?.length).map((mask) => mask.name)));
  for (const path of paths) {
    const name = relative(baselineDir, path).replaceAll("\\", "/");
    let candidate;
    try { candidate = await readFile(join(candidateDir, name)); } catch { results.push({ name, verdict: "missing" }); continue; }
    const [baseMasks, candidateMasks] = await Promise.all([sidecar(path), sidecar(join(candidateDir, name))]);
    const declared = [...(baseMasks?.masks ?? []), ...(candidateMasks?.masks ?? [])];
    const stale = [...new Set(declared.filter((mask) => mask.missing && !present.has(mask.name)).map((mask) => `${mask.name}: ${mask.reason || "selector matched no element"}`))];
    if (stale.length) { results.push({ name, verdict: "stale-mask", mask_errors: stale }); continue; }
    const skipped_masks = [...new Set(declared.filter((mask) => mask.missing && present.has(mask.name)).map((mask) => mask.name))].sort();
    const masks = trustedMasks(baseMasks, candidateMasks);
    const before = decodePNG(await readFile(path));
    const outcome = compareMasked(before, decodePNG(candidate), masks, { tolerance });
    outcome.masked_percent = +(maskedArea(masks, before.width, before.height) * 100).toFixed(2);
    const verdict = outcome.outside ? "unexplained" : outcome.inside ? "masked" : "match";
    results.push({ name, verdict, ...outcome, masks: masks.map((mask) => `${mask.name}×${mask.rects.length}`), skipped_masks, sidecars: { baseline: Boolean(baseMasks), candidate: Boolean(candidateMasks) } });
  }
  return results;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? "").href) {
  const args = process.argv.slice(2);
  const reportAt = args.indexOf("--report");
  const report = reportAt >= 0 ? args.splice(reportAt, 2)[1] : null;
  if (args.length !== 2) { console.error("usage: node tests/screenshot-gate.mjs BASELINE_DIR CANDIDATE_DIR [--report FILE]"); process.exit(2); }
  const results = await gate(args[0], args[1]);
  for (const r of results) {
    if (r.skipped_masks?.length) console.log(`SKIP MASK ${r.name}: ${r.skipped_masks.join(", ")} (element absent from this capture)`);
    if (r.verdict === "missing") console.log(`MISSING ${r.name}`);
    else if (r.verdict === "stale-mask") console.log(`STALE MASK ${r.name}: ${r.mask_errors.join("; ")}`);
    else if (r.verdict === "unexplained") console.log(`UNEXPLAINED ${r.name} ${r.outside} px outside authorized dynamic regions ${JSON.stringify(r.bounds)}${r.inside ? `; ${r.inside} px inside (${r.masksHit.join(", ")})` : ""}`);
    else if (r.verdict === "masked") console.log(`MATCH   ${r.name} (masked: ${r.masksHit.join(", ")}; ${r.inside} px inside masks, ${r.masked_percent}% of the image masked${r.rounding ? `, ${r.rounding} px one level apart` : ""})`);
    else console.log(`MATCH   ${r.name}${r.rounding ? ` (${r.rounding} px one level apart)` : ""}`);
  }
  const passed = results.filter((r) => r.verdict === "match" || r.verdict === "masked").length;
  const pass = passed === results.length && results.length > 0;
  console.log(`SCREENSHOT GATE ${pass ? "PASS" : "FAIL"} ${passed} of ${results.length} match${results.length ? "" : " (the baseline set is empty)"}`);
  if (report) await writeFile(report, `${JSON.stringify(results, null, 1)}\n`);
  process.exit(pass ? 0 : 1);
}
