// Item 2gj (v1.1.2/W7): an unelevated admin is still an admin. Three token
// shapes, three readouts, and only one of them is a refusal.
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const security = await readFile(new URL("./settings-security.js", import.meta.url), "utf8");
const script = await readFile(new URL("../../scripts/manage-signing.ps1", import.meta.url), "utf8");
const manager = await readFile(new URL("../../internal/signing/manager.go", import.meta.url), "utf8");

// The readout, evaluated exactly as the page evaluates it.
function adminStateText(state) {
  const body = security.slice(security.indexOf("function adminStateText"));
  const source = body.slice(0, body.indexOf("\n}") + 2);
  // eslint-disable-next-line no-new-func
  return Function(`${source}; return adminStateText(${JSON.stringify(state)});`)();
}

test("each of the three token states has its own readout", () => {
  assert.equal(adminStateText("elevated"), "administrator · elevated");
  assert.equal(adminStateText("not_elevated"), "administrator · signing needs an elevated run");
  assert.equal(adminStateText("not_admin"), "standard account · signing cannot be managed here");
});

test("only a non-administrator is refused; an unelevated admin is told what to do", () => {
  const unelevated = adminStateText("not_elevated");
  assert.doesNotMatch(unelevated, /cannot|may not|not allowed|denied/i, unelevated);
  assert.match(unelevated, /elevated run/);
  assert.match(adminStateText("not_admin"), /cannot be managed/);
});

test("membership that cannot be read is never a refusal", () => {
  // The item's narrowing consequence, in as many words.
  const unknown = adminStateText("unknown");
  assert.equal(unknown, "administrator status unknown until elevated");
  assert.doesNotMatch(unknown, /cannot|may not|denied/i);
  assert.equal(adminStateText(""), unknown);
  assert.equal(adminStateText(undefined), unknown);
});

test("the three states are produced by the script and carried by the status", () => {
  // The script asks "is elevated" FIRST, then group membership — the old check
  // asked only the first question and called an unelevated admin a non-admin.
  assert.match(script, /function Get-AdminState \{/);
  assert.match(script, /if \(Test-IsElevated\) \{ return 'elevated' \}/);
  assert.match(script, /if \(\$groups -match 'S-1-5-32-544'\) \{ return 'not_elevated' \}/);
  assert.match(script, /return 'not_admin'/);
  assert.match(script, /return 'unknown'/);
  assert.match(script, /admin_state = Get-AdminState/);
  // This item adds no elevation: reading which state the token is in never
  // raises UAC. The existing elevated apply path, which 2gj keeps, is the only
  // place that does, and it is outside this function.
  const body = script.slice(script.indexOf("function Get-AdminState"));
  assert.doesNotMatch(body.slice(0, body.indexOf("\n}") + 2), /Start-Process|RunAs/i);
  assert.match(manager, /AdminState\s+string\s+`json:"admin_state"`/);
});

test("the readout renders with a lamp that is alarm only for a standard account", () => {
  assert.match(security, /signingStatus\.admin_state === "elevated" \? "live" : signingStatus\.admin_state === "not_admin" \? "alarm" : ""/);
  assert.match(security, /adminStateText\(signingStatus\.admin_state\)/);
});
