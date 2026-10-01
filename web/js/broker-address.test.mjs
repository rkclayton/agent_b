import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

// Item 2nu (b): THE BROKER ADDRESS IS NOT IN THE PAGE. The operator's words, once the
// broker went public: "cant you just have the broker url hard coded in the app? we dont
// need to manipulate it or even expose it". Settings → Phone is the pairing, the paired
// device and Revoke — there is no field, no label and no read-out of the address.
const security = readFileSync(new URL("./settings-security.js", import.meta.url), "utf8");

test("Settings carries no broker field and no broker address", () => {
  assert.doesNotMatch(security, /broker\.url/, "the broker address is still an editable setting");
  assert.doesNotMatch(security, /wss:\/\//, "the page prints a broker address");
  assert.doesNotMatch(security, /text\("broker/, "the broker row is still a text field");
  // What the section DOES keep.
  assert.match(security, /broker-pair/);
  assert.match(security, /broker-revoke/);
  assert.match(security, /pairing-qr/);
});

test("the section says what it is for without naming an address", () => {
  const phone = security.slice(security.indexOf('subhead("Phone"'));
  assert.match(phone, /One phone/);
  assert.doesNotMatch(phone.slice(0, 2000), /agentb\.app/, "the subhead names the broker's host");
});
