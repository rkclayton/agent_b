#!/usr/bin/env node

import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { exactInputIdentity, loadPublishedProposal, markerText, packageMapStaleError, replaceCurrentOrderBody, validateProposal } from "./plan-lint.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const defaultRoot = path.resolve(here, "..");
const manifestName = ".plan-publication.json";
const staleIndexError = "PLAN.md index: stale or malformed; run node scripts/plan-lint.mjs --write-index --structural";

function normalize(relative) {
  return String(relative).replaceAll("\\", "/");
}

function sha256(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function readCandidate(candidate) {
  const planPath = path.join(candidate, "PLAN.md");
  if (!fs.existsSync(planPath)) throw new Error("candidate is missing PLAN.md");
  const files = [{ relative: "PLAN.md", text: fs.readFileSync(planPath, "utf8") }];
  for (const label of ["items", "archive"]) {
    const directory = path.join(candidate, "plan", label);
    if (!fs.existsSync(directory)) continue;
    for (const name of fs.readdirSync(directory).sort()) {
      const full = path.join(directory, name);
      if (!fs.statSync(full).isFile() || !name.endsWith(".md")) throw new Error(`candidate contains unsupported entry plan/${label}/${name}`);
      files.push({ relative: `plan/${label}/${name}`, text: fs.readFileSync(full, "utf8") });
    }
  }
  for (const relative of sideFiles) {
    const full = path.join(candidate, ...relative.split("/"));
    if (fs.existsSync(full)) files.push({ relative, text: fs.readFileSync(full, "utf8") });
  }
  return files;
}

// Item 1c: the side files a candidate may carry beside PLAN.md and the items.
const sideFiles = ["plan/_inflight.md", "plan/_history.md", "plan/_reference.md"];

function orderIdOf(planText) {
  return String(planText ?? "").match(/^Order ID:\s*`([^`]+)`/m)?.[1] ?? null;
}

// archiveOnSeal (item 1c): when a candidate carries a different order than the
// published one, the closed order's markers move verbatim from _inflight.md to
// the end of _history.md and _inflight.md starts empty. Nothing is rewritten:
// the history gains the lines exactly as they were written.
function archiveOnSeal(root, candidateFiles) {
  const published = loadPublishedProposal(root);
  const candidatePlan = candidateFiles.find(({ relative }) => relative === "PLAN.md")?.text ?? "";
  const before = orderIdOf(published.planText);
  const after = orderIdOf(candidatePlan);
  if (!before || !after || before === after) return candidateFiles;
  if (candidateFiles.some(({ relative }) => relative === "plan/_inflight.md" || relative === "plan/_history.md")) return candidateFiles;
  const text = (relative) => published.itemContents.find((entry) => entry.relative === relative)?.text ?? "";
  const inflight = text("plan/_inflight.md");
  if (!inflight.trim()) return candidateFiles;
  const history = text("plan/_history.md");
  const newline = history.includes("\r\n") ? "\r\n" : "\n";
  const joined = history.replace(/(?:\r?\n)*$/, "") + newline + newline + inflight.replace(/(?:\r?\n)*$/, "") + newline;
  return [...candidateFiles, { relative: "plan/_history.md", text: joined }, { relative: "plan/_inflight.md", text: "" }];
}

// splitInbox (item 1c): an INBOX may queue several PUBLISH THEN EXECUTE orders,
// separated by a line that is exactly the queue marker. Each order's body is
// the text between its ORDER BODY fences. The worker publishes and executes
// them in order; this only splits, it never publishes.
export const queueMarker = "==== NEXT ORDER ====";
export function splitInbox(text) {
  const blocks = String(text ?? "").replace(/\r\n/g, "\n").split(new RegExp("^" + queueMarker.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") + "\\s*$", "m"));
  return blocks.map((block) => block.replace(/^\s+|\s+$/g, "")).filter(Boolean).map((block, index) => {
    const body = block.match(/^---- ORDER BODY ----\s*$\n([\s\S]*?)^---- END ORDER BODY ----\s*$/m)?.[1]?.replace(/\s+$/, "") ?? null;
    return { index: index + 1, header: block.split("\n")[0], block, body };
  });
}

function mergedProposal(root, candidateFiles) {
  const published = loadPublishedProposal(root);
  const overrides = new Map(candidateFiles.filter(({ relative }) => relative !== "PLAN.md").map(({ relative, text }) => [normalize(relative), text]));
  const items = new Map(published.itemContents.map(({ relative, text }) => [normalize(relative), text]));
  for (const [relative, text] of overrides) items.set(relative, text);
  return {
    planText: candidateFiles.find(({ relative }) => relative === "PLAN.md").text,
    itemContents: [...items].map(([relative, text]) => ({ relative, text })),
    inputErrors: published.inputErrors,
    packageMap: published.packageMap,
    releaseTags: published.releaseTags,
  };
}

function publicationErrors(planText) {
  const count = [...String(planText).matchAll(/^## Current work order\b/gm)].length;
  return count === 1 ? [] : [`PUBLICATION: found ${count} Current work order bodies; expected exactly one`];
}

function indexedPlan(planText, indexSection, map = null) {
  const newline = planText.includes("\r\n") ? "\r\n" : "\n";
  const replacement = indexSection.replaceAll("\n", newline);
  if (!/^## Index\s*$/m.test(planText)) throw new Error("candidate PLAN.md has no ## Index section");
  const mapped = map === null ? planText : planText.replace(/<!-- package-map -->[\s\S]*?<!-- \/package-map -->/, () => map.replaceAll("\n", newline));
  return mapped.replace(/^## Index\s*$[\s\S]*$/m, () => replacement);
}

function writeExact(full, text) {
  fs.mkdirSync(path.dirname(full), { recursive: true });
  const temporary = `${full}.tmp-${process.pid}-${crypto.randomBytes(6).toString("hex")}`;
  fs.writeFileSync(temporary, text, { encoding: "utf8", flag: "wx" });
  fs.renameSync(temporary, full);
}

export function workerStopped(planText, itemContents = []) {
  const orderId = String(planText).match(/^Order ID:\s*`([^`]+)`/m)?.[1];
  if (!orderId) return { stopped: false, reason: "published plan has no Order ID" };
  const inFlight = markerText(planText, itemContents);
  const escaped = orderId.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const active = new Set();
  const markers = new RegExp(`^(?:-\\s*)?${escaped}/(W\\d+)\\s+(started|completed|stopped)\\b`, "gmi");
  for (const match of inFlight.matchAll(markers)) {
    if (match[2].toLowerCase() === "started") active.add(match[1].toUpperCase());
    else active.delete(match[1].toUpperCase());
  }
  return active.size ? { stopped: false, reason: `worker is active in ${[...active].join(", ")}` } : { stopped: true, reason: "no started-without-completion marker" };
}

function validationFailure(errors) {
  const error = new Error(`publication validation failed:\n${errors.map((value) => `- ${value}`).join("\n")}`);
  error.code = "VALIDATION_FAILED";
  return error;
}

// dryRunPublication answers the one question the planner cannot otherwise ask:
// what will the worker's gate say about this order body? It builds the same
// candidate prepare would, from the live published plan plus a body file and the
// current item files, runs every check, and writes nothing at all -- no candidate
// directory, no regenerated index, no seal.
export function dryRunPublication({ root = defaultRoot, body }) {
  root = path.resolve(root);
  const published = loadPublishedProposal(root);
  let planText = published.planText;
  let orderBody = null;
  if (body) {
    orderBody = fs.readFileSync(path.resolve(body), "utf8");
    // An order body as the planner writes it opens with its own
    // `## Current work order` heading; replaceCurrentOrderBody makes it the
    // plan's heading, so there is still exactly one body.
    planText = replaceCurrentOrderBody(planText, orderBody);
  }
  const proposed = { planText, itemContents: published.itemContents, inputErrors: published.inputErrors };
  const structural = validateProposal({ ...proposed, structuralOnly: true, inputErrors: [...proposed.inputErrors, ...publicationErrors(planText)], packageMap: published.packageMap });
  // prepare regenerates the index before the admission pass, so a stale index is
  // not a finding here either; everything else is reported exactly as it would be.
  const regenerate = structural.errors.includes(staleIndexError) || structural.errors.includes(packageMapStaleError);
  const indexed = regenerate ? indexedPlan(planText, structural.indexSection, published.packageMap) : planText;
  const result = validateProposal({ planText: indexed, itemContents: published.itemContents, inputErrors: [...published.inputErrors, ...publicationErrors(indexed)], packageMap: published.packageMap, releaseTags: published.releaseTags });
  const reported = result.errors.filter((message) => message !== staleIndexError && message !== packageMapStaleError);
  // A body for the NEXT order names a different order id while this order's
  // markers are still live, so the marker-ownership check fires every time a
  // planner checks ahead. It is a true statement and it is not about the body,
  // so it is reported separately rather than hidden or counted as a failure.
  const inFlight = reported.filter((message) => /^PLAN\.md: In flight marker .* belongs to another order/.test(message));
  const errors = reported.filter((message) => !inFlight.includes(message));
  return {
    ok: errors.length === 0,
    proposal_id: result.proposalId,
    base_id: validateProposal(published).proposalId,
    items: result.itemCount,
    index_regenerated: structural.errors.includes(staleIndexError),
    errors,
    expected_while_an_order_is_in_flight: inFlight,
    warnings: result.warnings,
    gated_items: result.completion.map(({ itemId, workIds }) => `${itemId}:${workIds.join("/")}`),
  };
}

export function preparePublication({ root = defaultRoot, candidate }) {
  root = path.resolve(root);
  candidate = path.resolve(candidate);
  if (root === candidate) throw new Error("candidate must be outside active plan state");
  const candidateFiles = archiveOnSeal(root, readCandidate(candidate));
  const proposed = mergedProposal(root, candidateFiles);
  const extraErrors = publicationErrors(proposed.planText);
  const first = validateProposal({ ...proposed, structuralOnly: true, inputErrors: [...proposed.inputErrors, ...extraErrors] });
  const nonIndexErrors = first.errors.filter((message) => message !== staleIndexError && message !== packageMapStaleError);
  if (nonIndexErrors.length) throw validationFailure(nonIndexErrors);

  const generatedPlan = indexedPlan(proposed.planText, first.indexSection, proposed.packageMap);
  const generatedFiles = candidateFiles.map((entry) => entry.relative === "PLAN.md" ? { ...entry, text: generatedPlan } : entry);
  const combined = mergedProposal(root, generatedFiles);
  const result = validateProposal({ ...combined, inputErrors: [...combined.inputErrors, ...publicationErrors(generatedPlan)] });
  if (result.errors.length) throw validationFailure(result.errors);

  writeExact(path.join(candidate, "PLAN.md"), generatedPlan);
  for (const entry of generatedFiles) if (sideFiles.includes(entry.relative)) writeExact(path.join(candidate, ...entry.relative.split("/")), entry.text);
  const base = loadPublishedProposal(root);
  const baseResult = validateProposal(base);
  const files = generatedFiles.map(({ relative, text }) => ({ relative, sha256: sha256(Buffer.from(text, "utf8")), bytes: Buffer.byteLength(text, "utf8") }));
  const manifest = {
    schema: 1,
    prepared_at: new Date().toISOString(),
    base_id: baseResult.proposalId,
    proposal_id: result.proposalId,
    files,
  };
  writeExact(path.join(candidate, manifestName), `${JSON.stringify(manifest, null, 2)}\n`);
  return { manifest, itemCount: result.itemCount, warnings: result.warnings };
}

function readSealedCandidate(candidate, manifest) {
  return manifest.files.map((record) => {
    const relative = normalize(record.relative);
    if (relative !== "PLAN.md" && !sideFiles.includes(relative) && !/^plan\/(?:items|archive)\/[^/]+\.md$/.test(relative)) throw new Error(`manifest contains unsupported path ${relative}`);
    const full = path.join(candidate, ...relative.split("/"));
    const bytes = fs.readFileSync(full);
    if (bytes.length !== record.bytes || sha256(bytes) !== record.sha256) throw new Error(`sealed candidate changed after validation: ${relative}`);
    return { relative, text: bytes.toString("utf8") };
  });
}

function publishTransaction(root, files) {
  const nonce = `${process.pid}-${crypto.randomBytes(6).toString("hex")}`;
  const records = files.map(({ relative, text }) => {
    const target = path.join(root, ...relative.split("/"));
    fs.mkdirSync(path.dirname(target), { recursive: true });
    const temporary = `${target}.publish-${nonce}`;
    const backup = `${target}.backup-${nonce}`;
    fs.writeFileSync(temporary, text, { encoding: "utf8", flag: "wx" });
    return { target, temporary, backup, existed: fs.existsSync(target), installed: false };
  });
  try {
    for (const record of records) {
      if (record.existed) fs.renameSync(record.target, record.backup);
      fs.renameSync(record.temporary, record.target);
      record.installed = true;
    }
  } catch (error) {
    for (const record of records.reverse()) {
      try { if (record.installed && fs.existsSync(record.target)) fs.rmSync(record.target, { force: true }); } catch {}
      try { if (record.existed && fs.existsSync(record.backup)) fs.renameSync(record.backup, record.target); } catch {}
      try { if (fs.existsSync(record.temporary)) fs.rmSync(record.temporary, { force: true }); } catch {}
    }
    throw error;
  }
  for (const record of records) if (record.existed && fs.existsSync(record.backup)) fs.rmSync(record.backup, { force: true });
}

export function publishPublication({ root = defaultRoot, candidate }) {
  root = path.resolve(root);
  candidate = path.resolve(candidate);
  const manifest = JSON.parse(fs.readFileSync(path.join(candidate, manifestName), "utf8"));
  if (manifest.schema !== 1 || !Array.isArray(manifest.files)) throw new Error("invalid publication manifest");
  const current = loadPublishedProposal(root);
  const stopped = workerStopped(current.planText, current.itemContents);
  if (!stopped.stopped) throw new Error(`publication refused: ${stopped.reason}`);
  const currentResult = validateProposal(current);
  if (currentResult.proposalId !== manifest.base_id) throw new Error("publication refused: active plan state changed after preparation");

  const files = readSealedCandidate(candidate, manifest);
  const combined = mergedProposal(root, files);
  const result = validateProposal({ ...combined, inputErrors: [...combined.inputErrors, ...publicationErrors(combined.planText)] });
  if (result.errors.length) throw validationFailure(result.errors);
  if (result.proposalId !== manifest.proposal_id) throw new Error("publication refused: validated proposal identity does not match the seal");
  publishTransaction(root, files);
  return { proposalId: result.proposalId, files: files.map(({ relative }) => relative), warnings: result.warnings };
}

// splitInboxTo writes each queued order's whole block and its body to --out,
// in queue order, so each can be dry-run, prepared and published on its own.
function splitInboxTo(inbox, out) {
  const orders = splitInbox(fs.readFileSync(path.resolve(inbox), "utf8"));
  fs.mkdirSync(path.resolve(out), { recursive: true });
  return orders.map(({ index, header, block, body }) => {
    const blockPath = path.join(path.resolve(out), `order-${index}.md`);
    fs.writeFileSync(blockPath, `${block}\n`, "utf8");
    const bodyPath = body === null ? null : path.join(path.resolve(out), `order-${index}-body.md`);
    if (bodyPath) fs.writeFileSync(bodyPath, `${body}\n`, "utf8");
    return { index, header, block: blockPath, body: bodyPath };
  });
}

function parseCLI(argv) {
  const command = argv[2];
  let root = defaultRoot;
  let candidate = "";
  let body = "";
  let dryRun = false;
  let inbox = "";
  let out = "";
  for (let index = 3; index < argv.length; index += 1) {
    if (argv[index] === "--root" && argv[index + 1]) root = argv[++index];
    else if (argv[index] === "--candidate" && argv[index + 1]) candidate = argv[++index];
    else if (argv[index] === "--body" && argv[index + 1]) body = argv[++index];
    else if (argv[index] === "--dry-run") dryRun = true;
    else if (argv[index] === "--inbox" && argv[index + 1]) inbox = argv[++index];
    else if (argv[index] === "--out" && argv[index + 1]) out = argv[++index];
    else throw new Error(`unknown argument: ${argv[index]}`);
  }
  if (command === "split-inbox") {
    if (!inbox || !out) throw new Error("split-inbox needs --inbox and --out");
    return { command, root, candidate, body, dryRun, inbox, out };
  }
  if (!dryRun && !candidate) throw new Error("--candidate is required");
  if (dryRun && command !== "prepare") throw new Error("--dry-run applies to prepare");
  return { command, root, candidate, body, dryRun };
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const { command, root, candidate, body, dryRun, inbox, out } = parseCLI(process.argv);
    const result = command === "split-inbox" ? splitInboxTo(inbox, out)
      : dryRun ? dryRunPublication({ root, body })
      : command === "prepare" ? preparePublication({ root, candidate })
        : command === "publish" ? publishPublication({ root, candidate })
          : (() => { throw new Error("command must be prepare, publish or split-inbox"); })();
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    if (dryRun && !result.ok) process.exitCode = 1;
  } catch (error) {
    process.stderr.write(`${error.message}\n`);
    process.exitCode = 1;
  }
}
