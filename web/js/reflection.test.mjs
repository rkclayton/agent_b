import assert from "node:assert/strict";
import test from "node:test";

import { loadReflection, reflectionLabel, reflectionText } from "./reflection.js";

const fakeDocument = () => {
  const elements = new Map([
    ["console-reflection-overview", { textContent: "" }],
    ["console-reflection-report", { textContent: "" }],
    ["console-reflection-when", { textContent: "" }],
  ]);
  return { getElementById: (id) => elements.get(id) || null, elements };
};

test("the reflection caption names the pass, its tier and the report", () => {
  assert.equal(reflectionLabel({ enabled: true, at: "2026-09-20T01:02:03Z", tier: "tier 1 (Go)", report_at: "2026-09-20T02:00:00Z" }),
    "overview 2026-09-20 01:02 UTC · tier 1 (Go) · report 2026-09-20 02:00 UTC");
  assert.equal(reflectionLabel({ enabled: true }), "after each run, and daily");
  assert.equal(reflectionLabel({}), "off on this install");
});

test("an install with no pass yet says so rather than showing an empty pane", () => {
  assert.deepEqual(reflectionText({ enabled: true }), { overview: "No reflection pass has run yet.", report: "" });
  assert.equal(reflectionText({}).overview, "Reflection is off on this install.");
});

test("Console fills both panes from the endpoint and adds no control", async () => {
  const doc = fakeDocument();
  const answer = { enabled: true, at: "2026-09-20T01:02:03Z", tier: "tier 1 (Go: go list)", overview: "Reflection on p1", report_at: "2026-09-20T01:05:00Z", report: "Tool candidates" };
  const loaded = await loadReflection(async () => ({ ok: true, json: async () => answer }), doc);
  assert.deepEqual(loaded, answer);
  assert.equal(doc.elements.get("console-reflection-overview").textContent, "Reflection on p1");
  assert.equal(doc.elements.get("console-reflection-report").textContent, "Tool candidates");
  assert.match(doc.elements.get("console-reflection-when").textContent, /tier 1/);
});

test("a failed fetch leaves the section as it was", async () => {
  const doc = fakeDocument();
  doc.elements.get("console-reflection-overview").textContent = "previous";
  assert.equal(await loadReflection(async () => ({ ok: false, status: 500 }), doc), null);
  assert.equal(doc.elements.get("console-reflection-overview").textContent, "previous");
  assert.equal(await loadReflection(async () => { throw new Error("offline"); }, doc), null);
});
