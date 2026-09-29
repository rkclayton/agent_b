// Item 2o5 (e): no real client organisation's name in the public repository's
// current tree, the release notes, or the published release pages.
//
// The deny-list lives OUTSIDE the repository and is never committed; its path is
// given by AGENTB_CLIENT_TERMS. One term per line, matched case-insensitively.
// A finding is reported as file, count, line numbers and the list line that
// matched — never the matching text, which would publish what it guards.
//
// Usage: node tools/check-client-terms.mjs [--releases] [extra files...]
//        node tools/check-client-terms.mjs --self-test   (CI: no list, a planted term)

import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";

const repo = path.resolve(import.meta.dirname, "..");

export function loadTerms(file) {
  return fs.readFileSync(file, "utf8").split(/\r?\n/)
    .map((term, index) => ({ term: term.trim().toLowerCase(), line: index + 1 }))
    .filter(({ term }) => term && !term.startsWith("#"));
}

export function scan(entries, terms) {
  const findings = [];
  for (const { name, text } of entries) {
    const hits = [];
    text.toLowerCase().split("\n").forEach((line, index) => {
      for (const { term, line: listLine } of terms) {
        for (let at = line.indexOf(term); at >= 0; at = line.indexOf(term, at + term.length)) hits.push({ line: index + 1, listLine });
      }
    });
    if (hits.length) findings.push({ name, count: hits.length, lines: [...new Set(hits.map((hit) => hit.line))], listLines: [...new Set(hits.map((hit) => hit.listLine))] });
  }
  return findings;
}

const git = (...args) => execFileSync("git", ["-C", repo, ...args], { maxBuffer: 1 << 28 }).toString();

function trackedEntries() {
  return git("ls-files", "-z").split("\0").filter(Boolean).flatMap((name) => {
    try { return [{ name, text: fs.readFileSync(path.join(repo, name), "utf8") }]; } catch { return []; }
  });
}

function releaseEntries() {
  const gh = (...args) => execFileSync("gh", args, { cwd: repo, maxBuffer: 1 << 28 }).toString();
  return JSON.parse(gh("release", "list", "--limit", "1000", "--json", "tagName")).map(({ tagName }) => {
    const release = JSON.parse(gh("release", "view", tagName, "--json", "name,body,assets"));
    return { name: `release ${tagName}`, text: [tagName, release.name, release.body, ...release.assets.map((asset) => asset.name)].join("\n") };
  });
}

function selfTest() {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), "client-terms-"));
  try {
    fs.writeFileSync(path.join(directory, "list.txt"), "# invented\nZyxplanted Org\n");
    fs.writeFileSync(path.join(directory, "fixture.json"), '{"label":"a"}\n{"label":"zyxplanted org, ZYXPLANTED ORG"}\n');
    const terms = loadTerms(path.join(directory, "list.txt"));
    const read = (name) => ({ name, text: fs.readFileSync(path.join(directory, name), "utf8") });
    const found = scan([read("fixture.json"), { name: "clean.json", text: "{}" }], terms);
    return found.length === 1 && found[0].name === "fixture.json" && found[0].count === 2 && found[0].lines[0] === 2 && found[0].listLines[0] === 2;
  } finally {
    fs.rmSync(directory, { recursive: true, force: true });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(import.meta.filename)) {
  const args = process.argv.slice(2);
  if (args.includes("--self-test")) {
    const ok = selfTest();
    console.log(ok ? "CLIENT TERMS SELF-TEST: a planted term was caught" : "CLIENT TERMS SELF-TEST FAILED: a planted term was not caught");
    process.exit(ok ? 0 : 1);
  }
  const list = process.env.AGENTB_CLIENT_TERMS;
  if (!list || !fs.existsSync(list)) {
    console.error("CLIENT TERMS: AGENTB_CLIENT_TERMS must name the operator's deny-list outside the repository");
    process.exit(2);
  }
  const entries = [...trackedEntries(), ...args.filter((arg) => !arg.startsWith("--")).map((name) => ({ name, text: fs.readFileSync(name, "utf8") })), ...(args.includes("--releases") ? releaseEntries() : [])];
  const findings = scan(entries, loadTerms(list));
  for (const finding of findings) console.error(`CLIENT TERMS: ${finding.name}: ${finding.count} match(es) on line(s) ${finding.lines.join(", ")} (list line ${finding.listLines.join(", ")})`);
  console.log(`CLIENT TERMS: ${entries.length} file(s) and page(s) scanned, ${findings.length} with a client term`);
  process.exit(findings.length ? 1 : 0);
}
