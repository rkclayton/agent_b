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
