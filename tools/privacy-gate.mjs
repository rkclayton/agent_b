#!/usr/bin/env node
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";

const repo = path.resolve(import.meta.dirname, "..");
const git = (...args) => execFileSync("git", ["-C", repo, ...args], { encoding: "utf8", maxBuffer: 1 << 28 });
const standInUsers = new Set(["someone", "someone-else"]);
const standInAddresses = new Set(["192.168.1.0", "192.168.1.10", "100.64.0.10"]);

export function resolveOutsideList({ required = true } = {}) {
  const configured = process.env.AGENTB_CLIENT_TERMS || (() => {
    try { return git("config", "--get", "agentb.clientTermsPath").trim(); } catch { return ""; }
  })();
  const base = process.env.AGENTB_HOME || path.join(process.env.USERPROFILE || os.homedir(), ".agentb");
  const file = configured || path.join(base, "client-terms.txt");
  if (!fs.existsSync(file)) {
    if (required) throw new Error("outside list missing");
    return null;
  }
  return file;
}

export function loadTerms(file) {
  return fs.readFileSync(file, "utf8").split(/\r?\n/)
    .map((term, index) => ({ term: term.trim().toLowerCase(), listLine: index + 1 }))
    .filter(({ term }) => term && !term.startsWith("#"));
}

function publicOccurrence(name, line, start, length) {
  if (/^(?:LICENSE|NOTICE)$/.test(name)) return true;
  const token = line.slice(start, start + length);
  const repoURL = /https:\/\/github\.com\/[^/\s]+\/agent_b/ig;
  for (const match of line.matchAll(repoURL)) if (start >= match.index && start + length <= match.index + match[0].length) return true;
  const thumbprints = /\b[0-9a-f]{40}\b/ig;
  for (const match of line.matchAll(thumbprints)) if (start >= match.index && start + length <= match.index + match[0].length) return true;
  return false;
}

function termOccursAt(line, term, at) {
  const word = /[a-z0-9]/i;
  const left = at === 0 ? "" : line[at - 1];
  const right = at + term.length >= line.length ? "" : line[at + term.length];
  if (word.test(term[0]) && word.test(left)) return false;
  if (word.test(term[term.length - 1]) && word.test(right)) return false;
  return true;
}

function generalFindings(name, line, lineNumber) {
  const out = [];
  const add = (rule, index = 0) => out.push({ name, line: lineNumber, rule, index });
  for (const match of line.matchAll(/[A-Za-z]:\\Users\\([^\\\s"'`,;(){}\[\]]+)/gi)) {
    if (!standInUsers.has(match[1].toLowerCase())) add("windows-home-path", match.index);
  }
  for (const match of line.matchAll(/\/(?:home|Users)\/([^/\s"'`,;(){}\[\]]+)/g)) {
    if (!standInUsers.has(match[1].toLowerCase())) add("posix-home-path", match.index);
  }
  for (const match of line.matchAll(/[a-z0-9._%+-]{2,}@([a-z0-9-]{2,}(?:\.[a-z0-9-]+)*\.[a-z]{2,})/gi)) {
    if (/^@\d+x\.png$/i.test(match[0].slice(match[0].indexOf("@")))) continue;
    if (!/(?:^|\.)example\.(?:org|com|net)$/i.test(match[1])) add("email", match.index);
  }
  for (const match of line.matchAll(/\b((?:\d{1,3}\.){3}\d{1,3})(?:\/(\d{1,2}))?/g)) {
    const parts = match[1].split(".").map(Number);
    if (parts.some((part) => part > 255) || standInAddresses.has(match[1])) continue;
    const privateAddress = parts[0] === 10 || (parts[0] === 172 && parts[1] >= 16 && parts[1] <= 31) || (parts[0] === 192 && parts[1] === 168) || (parts[0] === 100 && parts[1] >= 64 && parts[1] <= 127);
    if (privateAddress) add("private-or-shared-address", match.index);
  }
  for (const match of line.matchAll(/\b[a-z0-9-]+(?:\.[a-z0-9-]+)*\.ts\.net\b/gi)) add("tailnet-host", match.index);
  return out;
}

export function scanTextEntries(entries, terms = []) {
  const findings = [];
  for (const { name, text } of entries) {
    String(text).split(/\r?\n/).forEach((line, index) => {
      const lower = line.toLowerCase();
      for (const { term, listLine } of terms) {
        for (let at = lower.indexOf(term); at >= 0; at = lower.indexOf(term, at + Math.max(1, term.length))) {
          if (termOccursAt(lower, term, at) && !publicOccurrence(name, line, at, term.length)) findings.push({ name, line: index + 1, rule: "outside-list", listLine });
        }
      }
      findings.push(...generalFindings(name, line, index + 1));
    });
  }
  return findings;
}

function worktreeEntries() {
  return git("ls-files", "-z").split("\0").filter(Boolean).flatMap((name) => {
    try { return [{ name, text: fs.readFileSync(path.join(repo, name), "utf8") }]; } catch { return []; }
  });
}

function treeEntries(ref) {
  return git("ls-tree", "-r", "--name-only", "-z", ref).split("\0").filter(Boolean).flatMap((name) => {
    try { return [{ name, text: execFileSync("git", ["-C", repo, "show", `${ref}:${name}`], { encoding: "utf8", maxBuffer: 1 << 28 }) }]; } catch { return []; }
  });
}

function report(findings) {
  for (const finding of findings) {
    const list = finding.listLine ? ` list-line=${finding.listLine}` : "";
    process.stderr.write(`PRIVACY: ${finding.name}:${finding.line} rule=${finding.rule}${list}\n`);
  }
  process.stdout.write(`PRIVACY: scanned; findings=${findings.length}\n`);
  return findings.length ? 1 : 0;
}

export function scanBinary(files, terms) {
  const findings = [];
  for (const file of files) {
    const bytes = fs.readFileSync(file);
    const forms = [bytes.toString("latin1").toLowerCase(), bytes.toString("utf16le").toLowerCase()];
    for (const { term, listLine } of terms) {
      if (forms.some((text) => {
        for (let at = text.indexOf(term); at >= 0; at = text.indexOf(term, at + Math.max(1, term.length))) if (termOccursAt(text, term, at)) return true;
        return false;
      })) findings.push({ name: path.basename(file), line: 0, rule: "outside-list-bytes", listLine });
    }
    for (const [index, text] of forms.entries()) {
      const withoutDensityAssets = text.replace(/[a-z0-9._-]+@\d+x\.png/gi, "");
      findings.push(...generalFindings(path.basename(file), withoutDensityAssets, 0).map((finding) => ({ ...finding, rule: `${finding.rule}-bytes-${index + 1}` })));
    }
  }
  return findings;
}

function selfTest() {
  const privateWord = ["zyx", "private", "word"].join("-");
  const terms = [{ term: privateWord, listLine: 1 }];
  const entries = [
    { name: "source.go", text: [["C:", "Users", "NotAStandIn", "x"].join("\\"), ["someone", "invalid.test"].join("@"), [100, 65, 2, 3].join("."), privateWord].join("\n") },
    { name: "clean.go", text: "C:\\Users\\someone\\x\nsomeone@example.org\n100.64.0.10\n" },
  ];
  return scanTextEntries(entries, terms).length === 4;
}

if (process.argv[1] && path.resolve(process.argv[1]) === path.resolve(import.meta.filename)) {
  const args = process.argv.slice(2);
  if (args.includes("--self-test")) process.exit(selfTest() ? 0 : 1);
  const generalOnly = args.includes("--general-only");
  let terms = [];
  try { terms = generalOnly ? [] : loadTerms(resolveOutsideList()); }
  catch { process.stderr.write("PRIVACY REFUSED: outside list missing\n"); process.exit(2); }
  if (args.includes("--binary")) {
    const at = args.indexOf("--binary");
    process.exit(report(scanBinary(args.slice(at + 1), terms)));
  }
  if (args.includes("--pre-push")) {
    const lines = fs.readFileSync(0, "utf8").split(/\r?\n/).filter(Boolean);
    const refs = [...new Set(lines.map((line) => line.trim().split(/\s+/)[1]).filter((sha) => sha && !/^0+$/.test(sha)))];
    const entries = refs.flatMap((ref) => treeEntries(ref));
    for (const line of lines) {
      const [localRef, localSha, , remoteSha] = line.trim().split(/\s+/);
      if (!localSha || /^0+$/.test(localSha)) continue;
      if (localRef?.startsWith("refs/tags/")) {
        try {
          if (git("cat-file", "-t", localSha).trim() === "tag") entries.push({ name: `tag-message:${localRef.slice(10)}`, text: git("cat-file", "-p", localSha) });
        } catch { /* the tree/ref checks below will report an unusable object */ }
      }
      const range = remoteSha && !/^0+$/.test(remoteSha) ? `${remoteSha}..${localSha}` : localSha;
      entries.push({ name: `commit-messages:${localSha.slice(0, 12)}`, text: git("log", "--format=%B", range) });
    }
    process.exit(report(scanTextEntries(entries, terms)));
  }
  process.exit(report(scanTextEntries(worktreeEntries(), terms)));
}
