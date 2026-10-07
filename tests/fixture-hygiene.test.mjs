import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import zlib from "node:zlib";
import { annotationMessage, loadTerms, resolveOutsideList, scanTextEntries } from "../tools/privacy-gate.mjs";

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
function committedFiles(tree) {
  const listed = execFileSync("git", ["ls-files", "--", tree], { cwd: REPOSITORY, encoding: "utf8" });
  return listed.split(/\r?\n/).filter(Boolean);
}

function committedText(relative) {
  // Read the candidate bytes on disk. The push gate separately reads the exact
  // commit tree, while this test must also protect a scrub before it is staged.
  const bytes = fs.readFileSync(path.join(REPOSITORY, relative));
  return relative.endsWith(".gz") ? zlib.gunzipSync(bytes).toString("utf8") : bytes.toString("utf8");
}

test("no committed fixture carries a real identity or a real date", () => {
  const entries = [];
  for (const tree of FIXTURE_TREES) {
    if (!fs.existsSync(path.join(REPOSITORY, tree))) continue;
    for (const relative of committedFiles(tree)) {
      let text;
      try {
        text = committedText(relative);
      } catch {
        continue; // not in the index (a new file mid-change); the commit gate sees it next run
      }
      entries.push({ name: relative, text });
    }
  }
  const outside = resolveOutsideList({ required: false });
  const found = scanTextEntries(entries, outside ? loadTerms(outside) : []);
  assert.deepEqual(found, [], `committed fixtures carry real data at ${found.map((f) => `${f.name}:${f.line}:${f.rule}`).join(", ")}`);
});

test("the scrubber is idempotent, so running it twice is safe", async () => {
  const { scrubText } = await import("../tools/scrub-fixtures.mjs");
  const sample = `{"host":"invented-private-host","ts":"2026-09-23T17:12:37.006321600Z","later":"2026-09-23T17:13:54.723321600Z"}`;
  const once = scrubText(sample, null, ["invented-private-host"]);
  assert.equal(scrubText(once, null, ["invented-private-host"]), once);
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

test("the tag privacy gate scans annotation prose, not Git identity metadata", () => {
  const object = [
    "object 0123456789012345678901234567890123456789",
    "type commit",
    "tag v1.2.3",
    "tagger Private Person <private@example.invalid> 0 +0000",
    "",
    "Public release notes",
    "",
  ].join("\n");
  assert.equal(annotationMessage(object), "Public release notes\n");
});
