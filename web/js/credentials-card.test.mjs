import assert from "node:assert/strict";
import test from "node:test";
import { approvalText } from "./approval.js";
import { readFileSync } from "node:fs";

// Item 2nv (e): binding a credential, and moving a bound connector, each say so on the
// card the harness already draws — the name and the exact origin, and nothing else new.
test("the card names the credential and the exact origin", () => {
	const data = {
		name: "call_service",
		human: { happened: "call_service needs your approval.", harness_action: "The harness paused." },
		args: {
			connector: { operation: "add", entry: { name: "depot", auth: "stored:depot-key" } },
			connector_credential: "depot-key",
			connector_origin: "https://api.example.test:8443",
		},
	};
	const wording = approvalText(data);
	assert.match(wording.detail, /binds the credential "depot-key"/);
	assert.match(wording.detail, /https:\/\/api\.example\.test:8443/);
	assert.match(wording.detail, /not another port, not another host, and not a redirect off it/);

	const moved = approvalText({ ...data, args: { ...data.args, connector_previous_origin: "https://api.example.test:443" } });
	assert.match(moved.detail, /It was approved for https:\/\/api\.example\.test:443, and this moves it\./);

	// A connector with no credential says none of it.
	const plain = approvalText({ name: "call_service", args: { connector: { operation: "add", entry: { name: "depot" } } } });
	assert.doesNotMatch(plain.detail, /binds the credential/);
});

// Item 2nv (c): the section exists, the key field is masked, and no value is ever drawn.
test("Settings carries a Credentials section whose key field is masked", () => {
	const security = readFileSync(new URL("./settings-security.js", import.meta.url), "utf8");
	assert.match(security, /subhead\("Credentials"/);
	assert.match(security, /id="credential-secret" type="password"/);
	assert.match(security, /data-action="credential-add"/);
	assert.match(security, /data-action="credential-remove"/);
	// The listing prints name, origin, header and date — and no field called secret.
	const section = security.slice(security.indexOf("function credentialRows"), security.indexOf("function credentialRows") + 2000);
	assert.doesNotMatch(section, /entry\.secret|entry\.value/);
});
