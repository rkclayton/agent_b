export const gateArm = (name, status, detail = "") => {
  const allowed = new Set(["pass", "product", "prerequisite unavailable", "external"]);
  if (!allowed.has(status)) throw new Error(`unknown gate result class: ${status}`);
  return { name, status: status === "pass" ? "passed" : `not exercised: ${status}`, class: status, detail };
};

export const summarizeGate = (arms) => ({
  passed: arms.filter((arm) => arm.class === "pass").length,
  product_fail: arms.filter((arm) => arm.class === "product").length,
  not_exercised: arms.filter((arm) => arm.class === "prerequisite unavailable" || arm.class === "external").length,
  green: arms.length > 0 && arms.every((arm) => arm.class === "pass"),
  arms,
});
import fs from "node:fs";


export const summarizeGoTestJSON = (text) => {
  const packages = new Map();
  for (const line of text.split(/\r?\n/).filter(Boolean)) {
    const event = JSON.parse(line);
    if (!event.Package) continue;
    const summary = packages.get(event.Package) ?? { package: event.Package, run: 0, skipped: 0, failed: 0, status: "unknown" };
    const field = { run: "run", skip: "skipped", fail: "failed" }[event.Action];
    if (event.Test && field) summary[field] += 1;
    if (!event.Test && ["pass", "fail", "skip"].includes(event.Action)) summary.status = event.Action;
    packages.set(event.Package, summary);
  }
  return [...packages.values()].sort((left, right) => left.package.localeCompare(right.package));
};

if (process.argv[1]?.endsWith("gate-result.mjs") && process.argv[2] === "--go-json") {
  for (const summary of summarizeGoTestJSON(fs.readFileSync(process.argv[3], "utf8")))
    console.log(`PACKAGE ${summary.package}: run=${summary.run} skipped=${summary.skipped} failed=${summary.failed} status=${summary.status}`);
}
