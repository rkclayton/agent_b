import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { markerText, packageMap, releaseFindings, validateProposal } from "./plan-lint.mjs";
import { preparePublication, publishPublication, splitInbox, workerStopped } from "./plan-publish.mjs";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

// Item 1c: the plan's shape. Markers live in plan/_inflight.md and move to
// plan/_history.md when the next order is sealed; the package map is
// generated; RELEASE is PATCH by default and MINOR on a milestone step; INBOX
// may queue several orders.

const here = path.dirname(fileURLToPath(import.meta.url));
const linter = path.join(here, "plan-lint.mjs");
const roots = [];
process.on("exit", () => {
  for (const root of roots) if (path.basename(root).startsWith("agentb-plan-shape-")) removeTreeWithinAllowedRoots(root, [os.tmpdir()], "plan-shape test cleanup");
});

function item(id) {
  return ["state: live", "milestone: 0.2", "kind: feature", "surfaces: plan", "authorization: operator", "evidence: Fixture.", "acceptance: Fixture.", "", `# ${id} — fixture`, "", "## Unresolved", "", "(none)", ""].join("\n");
}

function plan(orderID, work, release = "RELEASE: none") {
  return ["# Plan", "", "## Architecture", "", "<!-- package-map -->", "<!-- /package-map -->", "", `## Current work order — ${orderID}`, "", `Order ID: \`${orderID}\``, "", work, "", release, "", "## In flight", "", "Markers: plan/_inflight.md.", "", "## Index", "", "placeholder", ""].join("\n");
}

function makeRoot(inflight) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-plan-shape-root-"));
  roots.push(root);
  for (const directory of ["plan/items", "plan/archive", "internal/alpha", "internal/beta", "web/js"]) fs.mkdirSync(path.join(root, directory), { recursive: true });
  fs.writeFileSync(path.join(root, "internal/alpha/alpha.go"), "// Package alpha does the first thing. It also does more.\npackage alpha\n");
  fs.writeFileSync(path.join(root, "internal/beta/beta.go"), "package beta\n");
  fs.writeFileSync(path.join(root, "web/js/one.js"), "// One module's purpose.\nexport const one = 1;\n");
  fs.writeFileSync(path.join(root, "web/js/two.js"), "export const two = 2;\n");
  fs.writeFileSync(path.join(root, "plan/items/2a.md"), item("2a"));
  fs.writeFileSync(path.join(root, "plan/_history.md"), "# History\n\nOLD/W1 completed 09:00\n");
  fs.writeFileSync(path.join(root, "plan/_inflight.md"), inflight);
  fs.writeFileSync(path.join(root, "PLAN.md"), plan("ONE", "- W1 **2a fixture work.**"));
  const written = spawnSync(process.execPath, [linter, "--root", root, "--write-index", "--structural"], { encoding: "utf8" });
  assert.equal(written.status, 0, written.stdout + written.stderr);
  return root;
}

// The marker text is the In flight section plus _inflight.md.
{
  const text = markerText("## In flight\n\nA/W1 started 10:00\n\n## Index\n", [{ relative: "plan/_inflight.md", text: "A/W1 completed 10:05\n" }]);
  assert.match(text, /A\/W1 started 10:00/);
  assert.match(text, /A\/W1 completed 10:05/);
  assert.equal(workerStopped("Order ID: `A`\n\n## In flight\n\n## Index\n", [{ relative: "plan/_inflight.md", text: "A/W2 started 11:00\n" }]).stopped, false, "a start marker in _inflight.md keeps publication refused");
}

// The package map lists every package and module, with or without a comment.
{
  const root = makeRoot("ONE/W1 started 10:00\nONE/W1 completed 10:30\n");
  const map = packageMap(root);
  assert.match(map, /- `internal\/alpha` — does the first thing\.\n/);
  assert.match(map, /- `internal\/beta`\n/);
  assert.match(map, /- `web\/js\/one\.js` — One module's purpose\.\n/);
  assert.match(map, /- `web\/js\/two\.js`\n/);
  assert.ok(fs.readFileSync(path.join(root, "PLAN.md"), "utf8").includes(map), "--write-index writes the map into PLAN.md");
  fs.writeFileSync(path.join(root, "internal/beta/beta.go"), "// Package beta does the second thing.\npackage beta\n");
  const stale = spawnSync(process.execPath, [linter, "--root", root, "--structural"], { encoding: "utf8" });
  assert.notEqual(stale.status, 0);
  assert.match(stale.stderr, /package map: stale/);

  // Sealing the next order moves ONE's markers verbatim to the history and
  // starts _inflight.md empty.
  const candidate = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-plan-shape-candidate-"));
  roots.push(candidate);
  fs.writeFileSync(path.join(candidate, "PLAN.md"), plan("TWO", "- W1 **2a next work.**"));
  preparePublication({ root, candidate });
  publishPublication({ root, candidate });
  assert.equal(fs.readFileSync(path.join(root, "plan/_inflight.md"), "utf8"), "");
  assert.equal(fs.readFileSync(path.join(root, "plan/_history.md"), "utf8"), "# History\n\nOLD/W1 completed 09:00\n\nONE/W1 started 10:00\nONE/W1 completed 10:30\n");
  assert.match(fs.readFileSync(path.join(root, "PLAN.md"), "utf8"), /internal\/beta` — does the second thing\./, "prepare regenerates the map");
}

// RELEASE: PATCH by default, MINOR on a milestone, each version follows the last.
{
  const tags = ["v0.68.0", "v0.69.0"];
  const ok = (text) => assert.deepEqual(releaseFindings(text, tags).errors, [], text);
  ok("RELEASE: PATCH");
  ok("RELEASE: MINOR (milestone: Plan page)");
  ok("RELEASE: none");
  ok("RELEASE: v0.69.0 (MINOR) at W12; v0.69.1 (PATCH) at W14; v0.70.0 (MINOR, milestone: Plan page) at W16");
  assert.deepEqual(releaseFindings("RELEASE: v0.69.1 (PATCH)", tags).warnings, []);
  assert.match(releaseFindings("RELEASE: v0.70.0 (MINOR)", tags).warnings[0], /MINOR without a milestone/);
  assert.match(releaseFindings("RELEASE: v0.69.2 (PATCH)", tags).errors[0], /does not follow v0\.69\.0; expected v0\.69\.1/);
  assert.match(releaseFindings("RELEASE: v0.70.1 (MINOR, milestone: x)", tags).errors[0], /expected v0\.70\.0/);
  assert.match(releaseFindings("RELEASE: v1.0.0 (MAJOR)", tags).errors[0], /hard stop 7/);
  assert.match(releaseFindings("RELEASE: SOMETIMES", tags).errors[0], /expected PATCH or MINOR/);
  const admission = validateProposal({ planText: plan("X", "- W1 **2a work.**", "RELEASE: v0.69.3 (PATCH)"), itemContents: [{ relative: "plan/items/2a.md", text: item("2a") }], releaseTags: tags });
  assert.ok(admission.admission.errors.some((message) => /RELEASE: v0\.69\.3/.test(message)), "the gate refuses a release that does not follow");
}

// INBOX may queue several orders; each body is split out whole.
{
  const one = "PUBLISH THEN EXECUTE — A\n\n---- ORDER BODY ----\n\n## Current work order — A\n\nbody A\n---- END ORDER BODY ----\n";
  const two = "PUBLISH THEN EXECUTE — B\n\n---- ORDER BODY ----\n\n## Current work order — B\n\nbody B\n---- END ORDER BODY ----\n";
  const orders = splitInbox(`${one}\n==== NEXT ORDER ====\n\n${two}`);
  assert.equal(orders.length, 2);
  assert.equal(orders[0].header, "PUBLISH THEN EXECUTE — A");
  assert.match(orders[0].body, /## Current work order — A\n\nbody A$/);
  assert.match(orders[1].body, /body B$/);
  assert.equal(splitInbox(one).length, 1, "a single order is a queue of one");
}

// Evidence rotation keeps the newest five releases and anything an open item
// cites, never touches unversioned directories, and removes nothing unasked.
{
  const { evidencePlan } = await import("./rotate-evidence.mjs");
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-plan-shape-evidence-"));
  roots.push(root);
  for (const name of ["2026-09-01-v0.1.0", "2026-09-02-v0.2.0", "v0.2.0-extra", "2026-09-03-v0.3.0", "2026-09-04-v0.4.0", "2026-09-05-v0.5.0", "2026-09-06-v0.6.0", "plan-snapshots", "2026-09-06-client-update"]) fs.mkdirSync(path.join(root, "logs", "evidence", name), { recursive: true });
  fs.mkdirSync(path.join(root, "plan", "items"), { recursive: true });
  fs.writeFileSync(path.join(root, "plan", "items", "3.md"), "evidence: logs/evidence/2026-09-01-v0.1.0/tape.jsonl\n");
  const plan = evidencePlan({ root, keep: 5 });
  assert.deepEqual(plan.remove, []);
  assert.deepEqual(plan.cited, ["2026-09-01-v0.1.0"]);
  assert.deepEqual(plan.unversioned, ["2026-09-06-client-update", "plan-snapshots"]);
  assert.deepEqual(evidencePlan({ root, keep: 4 }).remove, ["2026-09-02-v0.2.0", "v0.2.0-extra"]);
  assert.ok(fs.existsSync(path.join(root, "logs", "evidence", "2026-09-02-v0.2.0")), "listing removes nothing");
}
