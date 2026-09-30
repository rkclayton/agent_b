#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const requiredSections = ["Shipped", "Acceptance", "Misses", "Time", "Invented", "Cards", "Wall clock"];
const allowedInjections = ["compaction_constraints", "untrusted_tool_result", "agent_a_policy"];

export const checks = [
  ["universal-only", x => x.universal_scan === "pass"],
  ["ci-and-gate-map", x => x.ci === "pass" && x.gates?.selected?.length > 0 && x.gates?.skipped?.length > 0],
  ["always-and-release-gates", x => ["lint", "vet", "reconciliation", "production-incarnation"].every(v => x.gates?.always?.includes(v)) && (!x.release || x.gates?.full === true)],
  ["live-build-identity", x => x.live_identity?.tag === x.expected?.tag && x.live_identity?.commit === x.expected?.commit && x.live_identity?.dirty === false],
  ["capability-disposition", x => !x.capabilities?.some(v => v.status === "removed" && !v.replacement)],
  ["operator-requested-ui", x => !x.ui_control_added || /^Operator, .*“.+”$/.test(x.ui_evidence ?? "")],
  ["production-authority", x => !x.production?.mutated || (x.production?.owned === true && x.production?.authority === x.expected?.tag)],
  ["no-authored-operator-turn", x => x.operator_turns_authored === 0],
  ["prompt-injection-allowlist", x => x.prompt_injections?.every(v => allowedInjections.includes(v))],
  ["no-session-control", x => x.session_control_calls === 0],
  ["owned-exact-pid-stop", x => x.process_stops?.every(v => Number.isInteger(v.pid) && v.owned === true && !v.wildcard)],
  ["snapshot-contract", x => x.snapshot?.present === true && x.snapshot?.overwritten === false && x.snapshot?.newest_kept === 10],
  ["report-shape", x => requiredSections.every(v => x.report_sections?.includes(v)) && x.report_sections?.every(v => requiredSections.includes(v))],
  ["folded-rule-history", x => x.rules?.changed === x.rules?.archived_verbatim],
  ["acceptance-reconciliation", x => x.acceptance?.every(v => v.answer === "yes" || (v.answer === "no" && x.outcome === "shipped-partial"))],
  ["shipping-artifact", x => x.shipping_gates?.every(v => v.artifact === "shipping" && v.mode !== "dry-run")],
  ["prior-order-closed", x => x.prior?.report === true && x.prior?.final_marker === true],
  ["production-baseline", x => x.production?.baseline_ids?.every(v => x.production?.final_ids?.includes(v)) && ["version", "profile", "connections"].every(v => x.production?.baseline?.[v] === x.production?.final?.[v])],
  ["evidence-retention", x => x.evidence?.newest_kept === 5 && x.evidence?.cited_kept === true && x.evidence?.apply === false],
  ["structured-child-result", x => x.children?.every(v => Number.isInteger(v.exit_code) && v.payload_channel === "structured" && !v.parsed_combined_output)],
  ["bare-uac-path", x => x.uac_line == null || (/^[A-Za-z]:\\[^\r\n"']+\.cmd$/i.test(x.uac_line) && !/[&|]/.test(x.uac_line))],
  ["miss-ledger", x => Number.isInteger(x.misses?.count) && x.misses?.count === x.misses?.recount && !x.misses?.copied_forward],
  ["external-arms", x => x.external_arms?.every(v => /^not exercised: (external|prerequisite)$/.test(v.status) && v.reason)],
  ["rebaseline-after-change", x => !x.rebaseline || x.rebaseline.capture_order >= x.rebaseline.last_surface_order],
  ["invented-section", x => x.report_sections?.includes("Invented") && Array.isArray(x.invented)],
  ["check-wording-and-outcome", x => x.acceptance?.every(v => v.report_text === v.check_text) && (x.outcome !== "shipped" || x.acceptance?.every(v => v.answer === "yes"))],
  ["cross-repository-check", x => !x.cross_repository || /^plan\/CROSS-REPO\.md#\S+$/.test(x.cross_repository_check ?? "")],
];

export function policyFindings(input) {
  return checks.filter(([, accepts]) => !accepts(input)).map(([name]) => name);
}

export function readManifest(file) {
  return JSON.parse(fs.readFileSync(path.resolve(file), "utf8"));
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  if (process.argv.length !== 3) {
    process.stderr.write("usage: node tools/order-policy.mjs <order-evidence.json>\n");
    process.exit(2);
  }
  const failures = policyFindings(readManifest(process.argv[2]));
  if (failures.length) {
    process.stderr.write(`order policy failed: ${failures.join(", ")}\n`);
    process.exit(1);
  }
  process.stdout.write(`order policy passed (${checks.length} checks)\n`);
}
