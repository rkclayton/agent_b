import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import zlib from "node:zlib";

// Item 2m9 (a): A GATE, NOT A HABIT.
//
// It reads what GIT HAS rather than what the emitter meant to write, because the
// emitter is the thing being guarded: a fixture is only clean if the bytes in the
// repository are clean, and "we remember to scrub" is what let the operator's
// hostname into 51 places across ten committed tapes.
//
// It runs over the fixture trees rather than the whole repository, because the
// repository legitimately contains paths, hostnames and dates — this file does.
const REPOSITORY = path.resolve(import.meta.dirname, "..");
const FIXTURE_TREES = [
  "internal/projection/testdata/tapes",
  "internal/projection/testdata/pins/sources",
  "internal/projection/testdata/pins/golden",
];

// What may not appear in a committed fixture, and why each one matters.
const FORBIDDEN = [
  { name: "a Windows absolute path", pattern: /[A-Za-z]:\\\\?Users\\\\?/ },
  { name: "a POSIX home path", pattern: /\/(?:home|Users)\/[a-z][a-z0-9_-]{2,}/i },
  { name: "the operator's username", pattern: /\bRandy\b/i },
  { name: "a machine name", pattern: /\b(?:acme|acme)\b/i },
  { name: "an email address", pattern: /[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}/i },
  { name: "an http URL to a named host", pattern: /https?:\/\/(?!127\.0\.0\.1|localhost|example\.|fixture-)[a-z0-9-]+\.[a-z]{2,}/i },
  // (c): a calendar date. The fixtures are shifted to a fixed epoch, so any
  // year from 2015 on is a real one that escaped.
  { name: "a real calendar date", pattern: /\b20(?:1[5-9]|[2-9]\d)-[01]\d-[0-3]\d\b/ },
];

function committedFiles(tree) {
  const listed = execFileSync("git", ["ls-files", "--", tree], { cwd: REPOSITORY, encoding: "utf8" });
  return listed.split(/\r?\n/).filter(Boolean);
}

function committedText(relative) {
  // `git show :path` is the staged content — what a checkout would produce —
  // rather than whatever happens to be on disk right now.
  const bytes = execFileSync("git", ["show", `:${relative}`], { cwd: REPOSITORY, maxBuffer: 1 << 28 });
  return relative.endsWith(".gz") ? zlib.gunzipSync(bytes).toString("utf8") : bytes.toString("utf8");
}

test("no committed fixture carries a real identity or a real date", () => {
  const found = [];
  for (const tree of FIXTURE_TREES) {
    if (!fs.existsSync(path.join(REPOSITORY, tree))) continue;
    for (const relative of committedFiles(tree)) {
      let text;
      try {
        text = committedText(relative);
      } catch {
        continue; // not in the index (a new file mid-change); the commit gate sees it next run
      }
      for (const rule of FORBIDDEN) {
        const hit = rule.pattern.exec(text);
        if (hit) found.push(`${relative}: ${rule.name} — ${JSON.stringify(hit[0].slice(0, 60))}`);
      }
    }
  }
  assert.deepEqual(found, [], `committed fixtures carry real data:\n${found.join("\n")}`);
});

test("the scrubber is idempotent, so running it twice is safe", async () => {
  const { scrubText } = await import("../tools/scrub-fixtures.mjs");
  const sample = `{"host":"acme","ts":"2026-09-23T17:12:37.006321600Z","later":"2026-09-23T17:13:54.723321600Z"}`;
  const once = scrubText(sample);
  assert.equal(scrubText(once), once);
  // And it is length-preserving, which is what keeps every cursor offset valid.
  assert.equal(once.length, sample.length);
});

test("the scrubber keeps the exact distance between two instants", async () => {
  const { scrubText, nanosOf } = await import("../tools/scrub-fixtures.mjs");
  const sample = `{"a":"2026-09-23T17:12:37.006321600Z","b":"2026-09-23T17:13:54.723321600Z"}`;
  const moved = JSON.parse(scrubText(sample));
  const before = nanosOf("2026-09-23T17:13:54.723321600Z") - nanosOf("2026-09-23T17:12:37.006321600Z");
  assert.equal(nanosOf(moved.b) - nanosOf(moved.a), before);
});

test("a fixture already authored against 1970 is left alone", async () => {
  const { scrubText } = await import("../tools/scrub-fixtures.mjs");
  // Shifting these would move them forward fifty years and break every absolute
  // millisecond the projector derives from them.
  const synthetic = `{"ts":"1970-01-01T00:01:17.717Z"}`;
  assert.equal(scrubText(synthetic), synthetic);
});
