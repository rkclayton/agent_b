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

test("a discovered Entra connector card shows every public identity value before storing it", () => {
	const wording = approvalText({
		name: "call_service",
		args: {
			connector: { operation: "add", entry: { name: "work-api", auth: "entra:work-api" } },
			connector_credential: "work-api",
			connector_credential_kind: "entra",
			connector_origin: "https://api.example.test:443",
			connector_entra_tenant: "11111111-1111-1111-1111-111111111111",
			connector_entra_client_id: "22222222-2222-2222-2222-222222222222",
			connector_entra_scopes: ["https://api.example.test/mcp/access"],
		},
	});
	assert.match(wording.detail, /creates Microsoft Entra credential "work-api"/);
	assert.match(wording.detail, /11111111-1111-1111-1111-111111111111/);
	assert.match(wording.detail, /22222222-2222-2222-2222-222222222222/);
	assert.match(wording.detail, /https:\/\/api\.example\.test\/mcp\/access/);
	assert.match(wording.detail, /complete sign-in in Settings/);
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

test("Settings exposes explicit Entra browser, device-code, account, and sign-out controls without defaults", () => {
	const security = readFileSync(new URL("./settings-security.js", import.meta.url), "utf8");
	const controller = readFileSync(new URL("./settings.js", import.meta.url), "utf8");
	for (const action of ["credential-entra-add", "credential-sign-in", "credential-device-code", "credential-sign-out"]) {
		assert.match(security + controller, new RegExp(`data-action=["']${action}|action === ["']${action}`));
	}
	for (const field of ["credential-tenant", "credential-client-id", "credential-scopes"]) assert.match(security, new RegExp(`id=["']${field}`));
	assert.match(security, /entry\.account/);
	assert.doesNotMatch(security, /common|organizations|consumers|api:\/\/|00000000-0000/);
});
