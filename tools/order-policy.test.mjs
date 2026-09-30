import assert from "node:assert/strict";
import test from "node:test";
import { checks, policyFindings } from "./order-policy.mjs";

const valid = () => ({
  universal_scan: "pass", ci: "pass", release: true,
  gates: { selected: ["node"], skipped: ["playwright"], always: ["lint", "vet", "reconciliation", "production-incarnation"], full: true },
  expected: { tag: "v1.2.0", commit: "abc" }, live_identity: { tag: "v1.2.0", commit: "abc", dirty: false },
  capabilities: [{ status: "unchanged" }], ui_control_added: false,
  production: { mutated: false, baseline_ids: ["a"], final_ids: ["a", "b"], baseline: { version: "1", profile: "p", connections: "c" }, final: { version: "1", profile: "p", connections: "c" } },
  operator_turns_authored: 0, prompt_injections: ["compaction_constraints"], session_control_calls: 0,
  process_stops: [{ pid: 42, owned: true, wildcard: false }], snapshot: { present: true, overwritten: false, newest_kept: 10 },
  report_sections: ["Shipped", "Acceptance", "Misses", "Time", "Invented", "Cards", "Wall clock"],
  rules: { changed: 60, archived_verbatim: 60 }, outcome: "shipped",
  acceptance: [{ answer: "yes", check_text: "works", report_text: "works" }],
  shipping_gates: [{ artifact: "shipping", mode: "live" }], prior: { report: true, final_marker: true },
  evidence: { newest_kept: 5, cited_kept: true, apply: false },
  children: [{ exit_code: 0, payload_channel: "structured", parsed_combined_output: false }],
  uac_line: "C:\\stage\\install.cmd", misses: { count: 7, recount: 7, copied_forward: false },
  external_arms: [{ status: "not exercised: external", reason: "network unavailable" }],
  rebaseline: { capture_order: 2, last_surface_order: 1 }, invented: [], cross_repository: false,
});

const breakers = [
  x => x.universal_scan = "fail", x => x.ci = "fail", x => x.gates.always = [],
  x => x.live_identity.commit = "wrong", x => x.capabilities[0] = { status: "removed" }, x => { x.ui_control_added = true; x.ui_evidence = "inferred"; },
  x => x.production = { ...x.production, mutated: true }, x => x.operator_turns_authored = 1, x => x.prompt_injections.push("fourth_use"),
  x => x.session_control_calls = 1, x => x.process_stops[0].wildcard = true, x => x.snapshot.overwritten = true,
  x => x.report_sections.push("Narrative"), x => x.rules.archived_verbatim--, x => { x.acceptance[0].answer = "no"; x.outcome = "shipped"; },
  x => x.shipping_gates[0].mode = "dry-run", x => x.prior.report = false, x => x.production.final_ids = [], x => x.evidence.apply = true,
  x => x.children[0].parsed_combined_output = true, x => x.uac_line = "& 'C:\\stage\\install.cmd'", x => x.misses.recount = 6,
  x => x.external_arms[0].reason = "", x => x.rebaseline.capture_order = 0, x => x.invented = null,
  x => x.acceptance[0].report_text = "similar", x => { x.cross_repository = true; x.cross_repository_check = "missing"; },
];

test("all standing-rule checks accept a conforming order record", () => {
  assert.deepEqual(policyFindings(valid()), []);
  assert.equal(checks.length, 27);
});

test("every enforceable rule is red on its planted failure and no other rule masks it", () => {
  assert.equal(breakers.length, checks.length);
  breakers.forEach((breakRule, index) => {
    const input = valid();
    breakRule(input);
    assert.ok(policyFindings(input).includes(checks[index][0]), `negative control did not trip ${checks[index][0]}`);
  });
});
