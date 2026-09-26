import assert from "node:assert/strict";
import { releaseFindings } from "./plan-lint.mjs";
import crypto from "node:crypto";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { preparePublication, publishPublication, workerStopped } from "./plan-publish.mjs";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const here = path.dirname(fileURLToPath(import.meta.url));
const linter = path.join(here, "plan-lint.mjs");
const publisher = path.join(here, "plan-publish.mjs");
const linterHash = crypto.createHash("sha256").update(fs.readFileSync(linter)).digest("hex");
const roots = [];
process.on("exit", () => {
  for (const root of roots) if (path.basename(root).startsWith("agentb-plan-publish-")) removeTreeWithinAllowedRoots(root, [os.tmpdir()], "plan-publish test cleanup");
});

function item(id, { state = "live", authorization = "operator", acceptance = "Fixture behavior is verified." } = {}) {
  return [
    `state: ${state}`,
    "milestone: 0.2",
    "kind: feature",
    "surfaces: plan, tests",
    `authorization: ${authorization}`,
    "evidence: Operator-authorized publication fixture.",
    ...(acceptance === null ? [] : [`acceptance: ${acceptance}`]),
    ...(state === "shipped" ? ["shipped: v0.1.0 abcdef0"] : []),
    "",
    `# ${id} — fixture item`,
    "",
    "## Contract", "", "@budget net LOC ≤ +10, new files 0, new deps 0, new config keys 0", "", "Fixture body.",
    "",
    "## Unresolved",
    "",
    "(none)",
    "",
  ].join("\n");
}

function plan(order, inFlight = "TEST/W0 completed 12:00") {
  return [
    "# Plan fixture", "", "## Current work order — TEST", "", "Order ID: `TEST`", "", order, "",
    "## In flight", "", inFlight, "", "## Index", "", "placeholder", "",
  ].join("\n");
}

function makeRoot() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-plan-publish-root-"));
  roots.push(root);
  fs.mkdirSync(path.join(root, "plan", "items"), { recursive: true });
  fs.mkdirSync(path.join(root, "plan", "archive"), { recursive: true });
  fs.writeFileSync(path.join(root, "plan", "_reference.md"), "# References\n");
  fs.writeFileSync(path.join(root, "plan", "_history.md"), "# History\n");
  fs.writeFileSync(path.join(root, "plan", "items", "2a.md"), item("2a"));
  fs.writeFileSync(path.join(root, "PLAN.md"), plan("- W1 **2a existing work.**"));
  const prepared = spawnSync(process.execPath, [linter, "--root", root, "--write-index", "--structural"], { encoding: "utf8" });
  assert.equal(prepared.status, 0, prepared.stdout + prepared.stderr);
  return root;
}

function makeCandidate(root, order, entries = []) {
  const candidate = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-plan-publish-candidate-"));
  roots.push(candidate);
  fs.writeFileSync(path.join(candidate, "PLAN.md"), plan(order));
  for (const { where = "items", id, text } of entries) {
    fs.mkdirSync(path.join(candidate, "plan", where), { recursive: true });
    fs.writeFileSync(path.join(candidate, "plan", where, `${id}.md`), text);
  }
  return candidate;
}

function activeBytes(root) {
  const files = [path.join(root, "PLAN.md"), ...["items", "archive"].flatMap((label) => fs.readdirSync(path.join(root, "plan", label)).map((name) => path.join(root, "plan", label, name)))];
  return Object.fromEntries(files.sort().map((full) => [path.relative(root, full), fs.readFileSync(full, "utf8")]));
}

function expectPrepareFailure(root, candidate, pattern) {
  const before = activeBytes(root);
  assert.throws(() => preparePublication({ root, candidate }), pattern);
  assert.deepEqual(activeBytes(root), before, "failed preparation must leave active state byte-unchanged");
}

{
  const root = makeRoot();
  const withoutBudget = item("2b").replace(/^@budget.*\n/m, "");
  expectPrepareFailure(root, makeCandidate(root, "- W1 **2b missing budget.**", [{ id: "2b", text: withoutBudget }]), /PUBLICATION BUDGET: ordered item 2b/);
}

{
  const root = makeRoot();
  const candidate = makeCandidate(root, "- W1 **2a existing work.**\n- W2 **2b added work.**", [{ id: "2b", text: item("2b") }]);
  const prepared = preparePublication({ root, candidate });
  assert.match(prepared.manifest.proposal_id, /^sha256:[0-9a-f]{64}$/);
  assert.match(fs.readFileSync(path.join(candidate, "PLAN.md"), "utf8"), /\[2b\]\(plan\/items\/2b\.md\)/, "preparation must generate the candidate index");
  const published = publishPublication({ root, candidate });
  assert.deepEqual(published.files.sort(), ["PLAN.md", "plan/items/2b.md"]);
  assert.equal(fs.readFileSync(path.join(root, "PLAN.md"), "utf8"), fs.readFileSync(path.join(candidate, "PLAN.md"), "utf8"), "published PLAN must be the exact validated bytes");
  assert.equal(fs.readFileSync(path.join(root, "plan", "items", "2b.md"), "utf8"), fs.readFileSync(path.join(candidate, "plan", "items", "2b.md"), "utf8"), "published item must be the exact validated bytes");
  const ordinary = spawnSync(process.execPath, [linter, "--root", root], { encoding: "utf8" });
  assert.equal(ordinary.status, 0, ordinary.stdout + ordinary.stderr);
}

{
  const root = makeRoot();
  expectPrepareFailure(root, makeCandidate(root, "- W1 **2b invalid authorization.**", [{ id: "2b", text: item("2b", { authorization: "planner" }) }]), /invalid authorization/);
}

{
  const root = makeRoot();
  expectPrepareFailure(root, makeCandidate(root, "- W1 **2b missing acceptance.**", [{ id: "2b", text: item("2b", { acceptance: null }) }]), /missing required metadata acceptance/);
}

{
  const root = makeRoot();
  const candidate = makeCandidate(root, "- W1 **2b duplicate order body.**", [{ id: "2b", text: item("2b") }]);
  fs.appendFileSync(path.join(candidate, "PLAN.md"), "\n## Current work order — duplicate\n\nOrder ID: `OTHER`\n");
  expectPrepareFailure(root, candidate, /found 2 Current work order bodies/);
}

{
  const root = makeRoot();
  const candidate = makeCandidate(root, "- W1 **2a existing work.**");
  preparePublication({ root, candidate });
  fs.writeFileSync(path.join(root, "PLAN.md"), fs.readFileSync(path.join(root, "PLAN.md"), "utf8").replace("TEST/W0 completed 12:00", "TEST/W1 started 12:01"));
  const before = activeBytes(root);
  assert.throws(() => publishPublication({ root, candidate }), /worker is active in W1/);
  assert.deepEqual(activeBytes(root), before, "running-worker refusal must leave active state byte-unchanged");
}

{
  const root = makeRoot();
  const candidate = makeCandidate(root, "- W1 **2a existing work.**");
  preparePublication({ root, candidate });
  fs.appendFileSync(path.join(root, "PLAN.md"), "\n");
  const before = activeBytes(root);
  assert.throws(() => publishPublication({ root, candidate }), /active plan state changed after preparation/);
  assert.deepEqual(activeBytes(root), before, "changed-base refusal must leave active state byte-unchanged");
}

{
  const root = makeRoot();
  fs.writeFileSync(path.join(root, "plan", "archive", "2z.md"), item("2z", { state: "shipped" }));
  const candidate = makeCandidate(root, "- W1 **2z stale archived work.**");
  expectPrepareFailure(root, candidate, /RECONCILE: archived shipped item 2z is missing completed implementation marker\(s\): W1/);
}

assert.equal(workerStopped(plan("- W1 **2a work.**", "TEST/W1 started 12:00")).stopped, false);
assert.equal(workerStopped(plan("- W1 **2a work.**", "TEST/W1 started 12:00\nTEST/W1 completed 12:01")).stopped, true);
// v0.65.0/W0: CLAUDE.md closes a step with `stopped` as well as `completed`.
assert.equal(workerStopped(plan("- W1 **2a work.**", "TEST/W1 started 12:00\nTEST/W1 stopped 12:01 — unmet condition")).stopped, true);
assert.equal(crypto.createHash("sha256").update(fs.readFileSync(linter)).digest("hex"), linterHash, "plan-lint.mjs must remain byte-unchanged");
assert.equal(fs.existsSync(publisher), true);


// rel-1.16.0: an item may say it is measured and not capped, and be published --
// but only if it says WHY. Silence and a bare `none` are still refused, so the
// gate still means what it always meant: nothing ships without stating its
// budget. The order that forced this named an item it described itself as
// "capped by nothing", which the gate had no way to hear.
{
  const root = makeRoot();
  const uncapped = item("2b").replace(/^@budget.*$/m, "@budget  none — a documentation sweep has no honest cap in advance");
  const candidate = makeCandidate(root, "- W1 **2b uncapped work.**", [{ id: "2b", text: uncapped }]);
  const prepared = preparePublication({ root, candidate });
  assert.ok(String(prepared.manifest.proposal_id).startsWith("sha256:"), "an explicitly uncapped item must publish");
}

{
  const root = makeRoot();
  const bare = item("2b").replace(/^@budget.*$/m, "@budget  none");
  expectPrepareFailure(root, makeCandidate(root, "- W1 **2b bare none.**", [{ id: "2b", text: bare }]), /PUBLICATION BUDGET: ordered item 2b/);
}
process.stdout.write("plan publication fixtures passed\n");

// Item 2lp (a), (b) and (e). rel-1.17.0/W0 swept the closed orders with the old
// anchored matcher and found it read SEVEN OF NINE as having no declaration at
// all: every release from v1.13.0 to v1.15.0 shipped with the
// version-follows-the-tag check silently doing nothing, because those orders
// wrote `RELEASE:` mid-paragraph and bolded. A gate that cannot match its own
// documented input is a defect in the gate.
{
  const readable = (text, tags) => releaseFindings(text, tags).errors;

  // The form recent orders actually used, which the old pattern ignored.
  assert.deepEqual(readable("This order authorizes **2li**. RELEASE: **v1.15.0** (MINOR).", ["v1.14.0"]), [],
    "a bolded mid-paragraph declaration must be read");

  // The documented form still works.
  assert.deepEqual(readable("RELEASE: v1.17.0 (MINOR, milestone: 1.8).", ["v1.16.0"]), []);

  // And now that it is read, the check it was always meant to make actually fires.
  assert.match(readable("RELEASE: **v1.19.0** (MINOR).", ["v1.14.0"])[0] ?? "",
    /does not follow v1\.14\.0/, "the successor check must fire on a declaration it can now read");

  // (b): silence is a failure. This is the whole defect, as an assertion.
  assert.match(readable("This order ships v1.18.0 with no declaration anywhere.", ["v1.17.0"])[0] ?? "",
    /no declaration could be read/, "an order naming a release with no declaration must fail");

  // A discovery order that names no release is still fine, and `none` is still none.
  assert.deepEqual(readable("A discovery order that builds nothing.", ["v1.17.0"]), []);
  assert.deepEqual(readable("RELEASE: none", ["v1.17.0"]), []);
}
