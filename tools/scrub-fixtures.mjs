// Item 2m9 (b), (c) and (g): rewrite committed fixtures so they carry no real
// identity and no real calendar date, and do it as a SCRIPT rather than by hand
// because the mac repository holds the same twenty tapes and the planner has to
// carry this across.
//
// What it rewrites, and what it deliberately does not:
//
//   HOSTNAMES   the operator's machine names, case-insensitively, to a fixed
//               synthetic one. The tapes carried 51 occurrences across 10 of 21
//               files, which is the leak rel-1.23.0/W0 measured.
//
//   DATES       (c): the FIRST instant in each file moves to a fixed epoch and
//               every later timestamp keeps its exact offset from it. Ordering
//               and elapsed time are what the fixtures test; the wall-clock date
//               is not. A trailing `Z` stays `Z`, and a timestamp's precision --
//               milliseconds, or the nanoseconds the journals carry -- is
//               preserved digit for digit, because a projector that parses them
//               must see the same shape.
//
// It is idempotent: a file already scrubbed has no real hostname to find and its
// first instant is already the epoch, so a second run changes nothing.
import fs from "node:fs";
import path from "node:path";
import { loadTerms, resolveOutsideList } from "./privacy-gate.mjs";

export const EPOCH = "2020-01-01T00:00:00.000Z";

// The names to remove. Adding one here and re-running is how the next leak is
// closed; the gate in 2m9 (a) is what finds it.
function replacementFor(term) {
  if (term.includes("@")) return "someone@example.org";
  if (/^[a-z]:\\/i.test(term)) return "C:\\\\work\\\\acme";
  if (/^\/(?:home|Users)\//i.test(term)) return "/home/someone";
  if (/^S-1-/i.test(term)) return "S-1-5-21-1000-1000-1000-1001";
  if (/^100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./.test(term)) return "100.64.0.10";
  if (/^(?:10\.|172\.(?:1[6-9]|2\d|3[01])\.|192\.168\.)/.test(term)) return term.includes("/") ? "192.168.1.10/24" : "192.168.1.10";
  return "acme";
}

const TIMESTAMP = /(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?Z/g;

// The names are plain alphanumerics by construction -- a name needing regex
// escaping would be a hostname no operator has -- so the check is here rather
// than an escape nobody can read.
export function nanosOf(token) {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?Z$/.exec(token);
  if (!match) return null;
  const seconds = Date.UTC(+match[1], +match[2] - 1, +match[3], +match[4], +match[5], +match[6]) / 1000;
  const fraction = (match[7] ?? "").slice(1);
  return BigInt(seconds) * 1000000000n + BigInt((fraction + "000000000").slice(0, 9));
}

function formatNanos(nanos, fractionDigits) {
  const seconds = nanos / 1000000000n;
  const fraction = nanos % 1000000000n;
  const base = new Date(Number(seconds) * 1000).toISOString().slice(0, 19);
  if (!fractionDigits) return `${base}Z`;
  return `${base}.${fraction.toString().padStart(9, "0").slice(0, fractionDigits)}Z`;
}

// scrubText rewrites one file's text. `origin` is the case's first instant in
// NANOSECONDS; a file scrubbed alone falls back to its own.
//
// The arithmetic is BigInt nanoseconds rather than Date milliseconds, and that
// is not fussiness. The journals carry nanosecond timestamps, the projector
// derives RELATIVE values from them -- activity.stream.started_at is
// milliseconds since the run began -- and a shift computed at millisecond
// resolution moves the origin's sub-millisecond part without moving anything
// else. rel-1.23.0/W4 watched those relative values come out six milliseconds
// apart for exactly that reason.
export function scrubText(text, origin = null, terms = null) {
  let out = text;
  const deny = terms ?? loadTerms(resolveOutsideList()).map(({ term }) => term);
  for (const term of deny.sort((a, b) => b.length - a.length)) {
    out = out.replace(new RegExp(term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"), "gi"), replacementFor(term));
  }

  const instants = [...out.matchAll(TIMESTAMP)];
  if (!instants.length) return out;
  const first = origin ?? nanosOf(instants[0][0]);
  if (first === null || first === undefined) return out;
  // (c) says REAL dates become a fixed epoch. Some fixtures were authored
  // against 1970 already and carry no calendar date to leak; shifting those
  // moves them forward by fifty years and breaks every absolute millisecond the
  // projector derives from them — rel-1.23.0/W4 watched legacy-stream's
  // started_at go from 77717 to 1.577e12 for exactly that. A file whose first
  // instant predates 2015 is already synthetic and is left alone.
  if (first < nanosOf("2015-01-01T00:00:00.000Z")) return out;
  const epoch = nanosOf(EPOCH);
  const shift = epoch - first;

  // The projector also stores some instants as EPOCH MILLISECONDS -- a regex
  // over ISO text walks straight past those. Thirteen digits covers every date
  // from 2001 to 2286, so this substitution is length-preserving too, and the
  // window keeps it from catching an unrelated large number.
  out = out.replace(/(?<![\d.])1[5-9]\d{11}(?![\d.])/g, (digits) => {
    const moved = String(BigInt(digits) + shift / 1000000n);
    return moved.length === digits.length ? moved : digits;
  });

  return out.replace(TIMESTAMP, (match) => {
    const at = nanosOf(match);
    if (at === null) return match;
    const fractionDigits = (match.split(".")[1] ?? "").replace("Z", "").length;
    return formatNanos(at + shift, fractionDigits);
  });
}

export function firstInstant(text) {
  const match = TIMESTAMP.exec(text);
  TIMESTAMP.lastIndex = 0;
  return match ? nanosOf(match[0]) : null;
}

export function scrubTree(directory, { write = true, origin = null, origins = null } = {}) {
  const changed = [];
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    const full = path.join(directory, entry.name);
    if (entry.isDirectory()) {
      changed.push(...scrubTree(full, { write, origin, origins }));
      continue;
    }
    if (!/\.(json|jsonl|events)$/.test(entry.name)) continue;
    const before = fs.readFileSync(full, "utf8");
    const caseID = entry.name.replace(/\.(tape|golden)\.json$/, "").replace(/\.json$/, "");
    const after = scrubText(before, origins?.get(caseID) ?? origin);
    if (after === before) continue;
    if (write) fs.writeFileSync(full, after);
    changed.push({ file: full, bytesBefore: Buffer.byteLength(before), bytesAfter: Buffer.byteLength(after) });
  }
  return changed;
}

if (process.argv[1] && process.argv[1].endsWith("scrub-fixtures.mjs")) {
  const args = process.argv.slice(2);
  const pinsAt = args.indexOf("--pins");
  if (pinsAt >= 0) {
    const pins = args[pinsAt + 1];
    if (!pins) { console.error("--pins requires a directory"); process.exit(2); }
    const result = scrubPins(pins);
    for (const change of scrubTree(path.join(pins, "golden"), { origins: result.origins })) console.log(`regenerated ${change.file}`);
    console.log(`${result.touched.length} pin source(s) regenerated`);
    process.exit(0);
  }
  const roots = args;
  if (!roots.length) {
    console.error("usage: node tools/scrub-fixtures.mjs <fixture directory> [...]");
    process.exit(2);
  }
  let total = 0;
  for (const root of roots) {
    for (const change of scrubTree(root)) {
      console.log(`rewrote ${change.file} (${change.bytesBefore} → ${change.bytesAfter} bytes)`);
      total++;
    }
  }
  console.log(`${total} file(s) rewritten`);
}

// ---------------------------------------------------------------- pin sources
//
// Item 2m9's @verify anticipated this: the pin manifest records each source's
// sha256 and its DECOMPRESSED byte length, and rewriting text inside a gzipped
// source changes both. Rewriting the bytes without recomputing the manifest
// would leave the gate comparing a file against a fingerprint of the file it
// used to be -- a gate that fails for the right reason is fine, one that passes
// against a stale number is not.
import zlib from "node:zlib";
import crypto from "node:crypto";

export function scrubPins(pinsDirectory, { write = true } = {}) {
  const manifestPath = path.join(pinsDirectory, "manifest.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  const touched = [];
  // The case's origin comes from the SOURCE OF RECORD and is handed back so the
  // tape and the golden shift by the same amount. (b) calls this "the same
  // substitution applied across a tape and its golden and its pin source
  // together"; without it the three drift apart and the gate says so.
  const origins = new Map();
  for (const entry of manifest.cases ?? []) {
    const sourcePath = path.join(pinsDirectory, entry.source.split("/").join(path.sep));
    if (!fs.existsSync(sourcePath)) continue;
    const gzipped = sourcePath.endsWith(".gz");
    const raw = fs.readFileSync(sourcePath);
    const before = gzipped ? zlib.gunzipSync(raw).toString("utf8") : raw.toString("utf8");
    const origin = firstInstant(before);
    if (origin !== null) origins.set(entry.id, origin);
    const after = scrubText(before, origin);
    const decompressedBytes = Buffer.byteLength(after);
    // The gate reads the file as committed, so the fingerprint is of the file as
    // committed: the gzip for a compressed source, the text for a plain one.
    const committed = gzipped ? zlib.gzipSync(Buffer.from(after, "utf8"), { level: 9 }) : Buffer.from(after, "utf8");
    const digest = crypto.createHash("sha256").update(committed).digest("hex");
    const changed = after !== before || entry.origin_sha256 !== digest || entry.decompressed_bytes !== decompressedBytes;
    if (!changed) continue;
    if (write) {
      fs.writeFileSync(sourcePath, committed);
      entry.origin_sha256 = digest;
      entry.decompressed_bytes = decompressedBytes;
    }
    touched.push({ id: entry.id, source: entry.source, decompressedBytes });
  }
  if (write && touched.length) fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  return { touched, origins };
}
