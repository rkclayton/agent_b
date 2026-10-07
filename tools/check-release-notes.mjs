// Item 2n9 (d) and (g): the gate that keeps a release note readable by any user.
//
// It runs from deploy-release.ps1 BEFORE staging, so a note that breaks the rule
// stops the release rather than being discovered after it is published — which is
// what @verify asked to be established. It also runs in the node suite over every
// note written under the rule.
//
// Two rules, both from the item:
//
//   (d) no banned vocabulary. The list lives in release-notes/BANNED-VOCABULARY.txt
//       and the operator extends it by adding a line; nothing here changes.
//   (g) the form is a BRIEF BULLET LIST. One bullet per change, one line each. No
//       narrative, no paragraphs. So: outside a bullet, the only things allowed are
//       a heading, a blank line, or a fenced block — any other prose line fails.
//
// Usage: node tools/check-release-notes.mjs <notes file> [more files...]
//        node tools/check-release-notes.mjs --all     (every note under the rule)

import fs from "node:fs";
import path from "node:path";
import { loadTerms, resolveOutsideList } from "./privacy-gate.mjs";

const repo = path.resolve(import.meta.dirname, "..");
const notesDir = path.join(repo, "release-notes");
const listPath = path.join(notesDir, "BANNED-VOCABULARY.txt");

// The first release written under item 2n9. Notes older than this were written to
// one reader and are left as the historical record they are.
export const FIRST_VERSION_UNDER_THE_RULE = [1, 24, 0];

export function versionOf(file) {
  const match = path.basename(file).match(/^v(\d+)\.(\d+)\.(\d+)\.md$/);
  return match ? match.slice(1).map(Number) : null;
}

export function underTheRule(file) {
  const version = versionOf(file);
  if (!version) return false;
  for (let index = 0; index < 3; index++) {
    if (version[index] !== FIRST_VERSION_UNDER_THE_RULE[index]) {
      return version[index] > FIRST_VERSION_UNDER_THE_RULE[index];
    }
  }
  return true;
}

export function notesUnderTheRule() {
  return fs.readdirSync(notesDir)
    .filter((name) => underTheRule(name))
    .sort()
    .map((name) => path.join(notesDir, name));
}

export function bannedPatterns(source = listPath) {
  const lines = fs.readFileSync(source, "utf8").split(/\r?\n/);
  const patterns = [];
  for (const raw of lines) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    if (line.startsWith("/") && line.endsWith("/") && line.length > 2) {
      patterns.push({ entry: line, regex: new RegExp(line.slice(1, -1), "i") });
      continue;
    }
    // A plain entry matches as a whole word, so "order" does not fire on "ordered"
    // and "scratch" does not fire on "scratchpad".
    const escaped = line.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    patterns.push({ entry: line, regex: new RegExp(`(^|[^A-Za-z0-9])${escaped}([^A-Za-z0-9]|$)`, "i") });
  }
  const outside = resolveOutsideList({ required: false });
  if (outside) {
    for (const { term, listLine } of loadTerms(outside)) {
      const escaped = term.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
      patterns.push({ entry: `outside-list line ${listLine}`, regex: new RegExp(escaped, "i") });
    }
  }
  return patterns;
}

// (g): what a line is allowed to be when it is not a bullet.
function offendingProse(lines) {
  const problems = [];
  let inFence = false;
  lines.forEach((line, index) => {
    const text = line.trim();
    if (text.startsWith("```")) {
      inFence = !inFence;
      return;
    }
    if (inFence || text === "") return;
    if (text.startsWith("#")) return;
    if (/^[-*]\s+\S/.test(text)) {
      return;
    }
    if (/^\|/.test(text)) return;
    problems.push({ line: index + 1, text });
  });
  return problems;
}

export function checkNotes(file, patterns = bannedPatterns()) {
  const body = fs.readFileSync(file, "utf8");
  const lines = body.split(/\r?\n/);
  const failures = [];
  lines.forEach((line, index) => {
    if (line.trim().startsWith("#")) {
      // A heading is still held to the vocabulary, just not to the prose rule.
    }
    for (const { entry, regex } of patterns) {
      if (regex.test(line)) {
        failures.push({ kind: "vocabulary", line: index + 1, entry, text: line.trim().slice(0, 100) });
      }
    }
  });
  for (const prose of offendingProse(lines)) {
    failures.push({ kind: "prose", line: prose.line, entry: "(g) one bullet per change, no narrative", text: prose.text.slice(0, 100) });
  }
  return failures;
}

function main(argv) {
  const files = argv.includes("--all")
    ? notesUnderTheRule()
    : argv.filter((value) => !value.startsWith("--")).map((value) => path.resolve(value));
  if (!files.length) {
    console.error("usage: node tools/check-release-notes.mjs <notes file> | --all");
    return 2;
  }
  const patterns = bannedPatterns();
  let bad = 0;
  for (const file of files) {
    const failures = checkNotes(file, patterns);
    if (!failures.length) {
      console.log(`NOTES OK: ${path.basename(file)}`);
      continue;
    }
    bad += failures.length;
    console.error(`NOTES REFUSED: ${path.basename(file)}`);
    for (const failure of failures) {
      console.error(`  line ${failure.line}: ${failure.kind} — ${failure.entry}`);
      console.error("    matching text withheld");
    }
  }
  if (bad) {
    console.error(`\n${bad} problem(s). A release note is read by anyone who runs Agent_b:`);
    console.error("  * say what changed and why it matters, one bullet each;");
    console.error("  * name no machine, folder, person, repository or internal process;");
    console.error("  * put everything this forbids in NOTES.md instead.");
    console.error(`  * the word list is ${path.relative(repo, listPath)} and takes one line per entry.`);
  }
  return bad ? 1 : 0;
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(import.meta.filename)) {
  process.exit(main(process.argv.slice(2)));
}
