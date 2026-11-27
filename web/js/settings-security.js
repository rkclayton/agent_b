let serviceAccountLog = "";
let brokerStatus = {};
let brokerMessage = "";
let brokerAlarm = false;
let credentialList = [];
let browserDataBytes = null, browserDataConfirm = false;
let credentialMessage = "";
let credentialAlarm = false;
let credentialDevice = {};
let credentialProvider = "entra";
let credentialKind = "";
let store, armed, drafts, errors, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, serviceAccountSteps, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, standingGrants, connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView;
function useSettingsContext(context) {
  serviceAccountLog = context.serviceAccountLog || "";
  brokerStatus = context.brokerStatus || {};
  brokerMessage = context.brokerMessage || "";
  brokerAlarm = !!context.brokerAlarm;
  credentialList = context.credentialList || [];
  browserDataBytes = context.browserDataBytes;
  browserDataConfirm = !!context.browserDataConfirm;
  credentialMessage = context.credentialMessage || "";
  credentialAlarm = !!context.credentialAlarm;
  credentialDevice = context.credentialDevice || {};
  credentialProvider = context.credentialProvider === "google" ? "google" : "entra";
  credentialKind = context.credentialKind || "";
  ({ store, armed, drafts, errors, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, serviceAccountSteps = [], hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, standingGrants = [], connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView } = context);
}

function shell(active) {
	const service = store.config.shell?.service_account || {};
	const credential = store.shell_credential || {};
	const stored = credential.stored
		? `stored ${credential.stored_at || "(time unavailable)"}`
		: "not stored";
	const accountState = !serviceAccountStatus.loaded
		? "checking local account…"
		: !serviceAccountStatus.supported
			? "local account setup is available only on Windows"
			: serviceAccountStatus.state === "ready"
				? "agentb-svc · account and credential ready"
				: serviceAccountStatus.state === "locked_out"
					? "agentb-svc · account locked out · wait, then Repair"
				: serviceAccountStatus.state === "missing_credential"
					? "agentb-svc · account exists · credential missing"
					: serviceAccountStatus.state === "invalid_credential"
						? "agentb-svc · account exists · credential rejected"
						: serviceAccountStatus.state === "credential_check_failed"
							? "agentb-svc · credential check temporarily unavailable"
						: serviceAccountStatus.state === "administrator"
							? "agentb-svc · ADMINISTRATOR — refused"
						: serviceAccountStatus.state === "disabled"
							// the account EXISTS and the identity is off by
							// choice. This state fell through to "account not created" —
							// the operator read that beside a working account and pressed
							// Repair, which is what the words told him to do.
							? "agentb-svc · account exists · identity off"
						: serviceAccountStatus.state === "missing"
							? "agentb-svc · account not created"
							: `agentb-svc · ${serviceAccountStatus.state || "state unknown"}`;
	const setupLabel = serviceAccountBusy
		? "Waiting for Windows UAC…"
		: serviceAccountStatus.state === "locked_out"
			? "Repair unavailable — account locked out"
		: serviceAccountStatus.action || (serviceAccountStatus.exists ? "Repair" : "Set up");
	const connection = connectionList().find((item) => item.id === selectedHardeningConnectionID());
	const setupDisabled = serviceAccountBusy || !serviceAccountStatus.loaded || !serviceAccountStatus.supported || serviceAccountStatus.administrator || serviceAccountStatus.state === "locked_out" || !connection;
	const protectionReady = hardeningStatus.acl?.applied && hardeningStatus.firewall?.applied;
	const elevationState = !hardeningStatus.loaded
		? "checking process elevation…"
		: hardeningStatus.harness_elevated
			? "already elevated · Windows will not show UAC"
			: "standard user token · Windows may request UAC";
	const protectionState = !hardeningStatus.loaded
		? "checking host protections…"
		: !hardeningStatus.supported
			? "available only on Windows"
			: protectionReady
				? "ACL + outbound policy verified"
				: `${hardeningStatus.acl?.summary || "ACL not applied"} · ${hardeningStatus.firewall?.summary || "firewall not applied"}`;
	const driftLines = [...(hardeningStatus.acl?.items || []), ...(hardeningStatus.firewall?.items || [])]
		.map((item) => `<li>${html(item.path || item.rule || "policy")} · expected ${html(item.expected || "applied")} · found ${html(item.found || "missing")}</li>`).join("");
	const applyBlocker = drafts.size
		? "Save pending settings before applying host protections."
		: serviceAccountBusy
			? "Wait for the service-account operation to finish."
			: !service.enabled
				? "Enable the service identity and save first."
				: !credential.stored
					? "Store the service-account credential first."
					: !serviceAccountStatus.exists
						? "Create the service account first."
						: serviceAccountStatus.administrator
							? "The service account is an Administrator and cannot be used."
							: !connection
								? "Test and select a runnable model connection first."
								: "";
	const canApply = !hardeningBusy && !applyBlocker;
	const canInspect = !hardeningBusy && hardeningStatus.loaded && hardeningStatus.supported && serviceAccountStatus.exists;
	const canTestIdentity = !serviceAccountBusy && credential.stored && protectionReady;
	const protectionFeedback = applyBlocker
		? `Apply unavailable: ${applyBlocker}${hardeningMessage ? ` Last result: ${hardeningMessage}` : ""}`
		: hardeningMessage;
	const operatorView = operatorStatusView({
		...(store.shell_identity || {}),
		operator_context: store.config.shell?.operator_context ?? store.shell_identity?.operator_context,
	});
	const lanEnabled = !!store.config.shell?.allow_local_network;
	const sandboxEnabled = !!store.config.sandbox?.enabled;
	const sandboxStatus = store.sandbox || {};
	const sandboxState = sandboxStatus.available ? "ready" : (sandboxStatus.reason || "Docker Sandbox is unavailable");
	const trustedFolders = Array.isArray(store.config.shell?.trusted_folders) ? store.config.shell.trusted_folders : [];
	const trustedRows = trustedFolders.map((entry, index) => { const path = String(entry.path || ""), valid = /^(?:[a-z]:[\\/]|\\\\|\/)/i.test(path); return `<tr><td><span id="trusted-folder-${index}" class="allowed-directory-path" contenteditable="plaintext-only" role="textbox" aria-label="Allowed directory" title="${attr(path)}">${html(path)}</span>${valid ? "" : '<span class="allowed-directory-invalid">not a path</span>'}</td><td>${html((entry.added_at || "").slice(0, 10))} · ${entry.source === "card" ? "card" : "Settings"}</td><td><button type="button" data-action="trusted-folder-change" data-id="${index}">Change</button><button type="button" data-action="trusted-folder-delete" data-id="${index}">Delete</button></td></tr>`; }).join("");
	const confirmedSubnets = new Set(store.config.shell?.confirmed_local_subnets || []);
	const detectedSubnets = hardeningStatus.detected_local_subnets || [];
	const subnetChoices = detectedSubnets.length
		? detectedSubnets.map((subnet) => `<label class="settings-chip"><input type="checkbox" data-local-subnet value="${attr(subnet)}" ${confirmedSubnets.has(subnet) ? "checked" : ""} ${lanEnabled ? "" : "disabled"}> ${html(subnet)}</label>`).join("")
		: '<span class="settings-chip-empty">none detected</span>';
	// OFF IS NOT AN ERROR. The panel opened whenever the state was not
	// ready, which includes the identity being off by choice — so a deliberate setting
	// looked like a fault with a repair panel hanging open under it. It opens when
	// something is actually wrong with an identity that is on, or being turned on.
	const identityOn = !!service.enabled;
	const setupOpen = (identityOn || serviceAccountBusy) && serviceAccountStatus.state !== "ready";
	const identityAlarm = (identityOn || serviceAccountBusy) && serviceAccountStatus.loaded && serviceAccountStatus.state !== "ready";
	const serviceLogHref = serviceAccountLog ? encodeURI(`file:///${serviceAccountLog.replaceAll("\\", "/")}`) : "";
	const serviceStepLines = serviceAccountSteps.map((line) => `<p class="account-status">${html(line)}</p>`).join("");
  return `${subhead("Run as you", "Run everything as you for 20 minutes. This is the one line that stays visible because misreading it is dangerous.")}
	${row("identity", `<button type="button" class="settings-operator-status" data-action="operator-context" aria-pressed="${operatorView.active}" aria-label="${attr(operatorView.label)}"><img src="${operatorView.src}" srcset="${operatorView.srcset}" width="24" height="24" alt=""><span>${operatorView.active ? "Stop running everything as me" : "Run everything as me for 20 minutes"}</span></button>`, "", "Runs every tool as you, without the service account's limits, for 20 minutes or until you stop it.")}
	<p class="settings-note">This defeats the service-account OS boundary for every tool in every chat until it expires.</p>
	${subhead("Allowed Directories", "A directory and its descendants skip outside-folder prompts.")}
	<table class="allowed-directories"><thead><tr><th>Directory</th><th>Added</th><th></th></tr></thead><tbody>${trustedRows}<tr><td><input id="trusted-folder-new" placeholder="C:\\folder" aria-label="Add allowed directory"></td><td>—</td><td><button type="button" data-action="trusted-folder-add">Add</button></td></tr></tbody></table>
	${errors.get("shell.trusted_folders") ? `<p class="settings-feedback alarm">${html(errors.get("shell.trusted_folders"))}</p>` : ""}
	${brokerRows()}
	${credentialRows()}
	${subhead("Experimental", "")}
	<div class="experimental-label" title="Use the restricted Windows account for tools.">Service identity</div>
	${row("Service identity", `<button type="button" role="switch" aria-checked="${identityOn}" aria-label="Service identity" class="switch ${identityOn ? "on" : ""}" data-action="service-identity-toggle" ${serviceAccountBusy ? "disabled" : ""}></button><span class="account-status">${serviceAccountBusy ? "Turning on — Windows will ask once" : identityOn ? "on" : "off"}</span>`, "", "Off: tools run as the account that launched Agent_b. On: Agent_b runs tools as its own restricted Windows account.")}
	${feedback(serviceAccountMessage, serviceAccountAlarm)}
	${serviceAccountStatus.execution_policy_message ? `<p class="settings-feedback alarm" role="status">${html(serviceAccountStatus.execution_policy_message)}</p>` : ""}
	${serviceStepLines}
	${serviceAccountMessage && serviceAccountLog ? `<p class="account-status"><a href="${attr(serviceLogHref)}">open log</a> · ${html(serviceAccountLog)}</p>` : ""}
	<details class="settings-advanced"${setupOpen ? " open" : ""}>
	  <summary>Set up service identity</summary>
	  <p class="settings-note">Agent_b generates and stores the password. One Windows approval creates or repairs the account, folder access and outbound policy, then tests the credential before enabling it.</p>
	  ${store.shell_identity?.notice ? `<p class="settings-feedback alarm" role="status">${html(store.shell_identity.notice)}</p>` : ""}
	  ${row("account", `<span class="account-status"><span class="lamp ${serviceAccountStatus.state === "ready" ? "live" : identityAlarm ? "alarm" : ""}"></span>${html(accountState)}</span>`)}
	  ${row("credential", `<span class="account-status">${html(stored)}</span>`)}
	  <div class="settings-minor-head" title="Folder access and outbound policy applied to the service identity.">Host protections</div>
	  ${row("protections", `<span class="account-status"><span class="lamp ${protectionReady ? "live" : hardeningStatus.loaded ? "alarm" : ""}"></span>${html(protectionState)}</span>`)}
	  ${driftLines ? `<ul class="settings-drift">${driftLines}</ul>` : ""}
	  ${row("model route", `<select id="hardening-connection" aria-label="Model route for host protections">${hardeningConnections()}</select>`)}
	  ${row("Allow my local network", `<button type="button" role="switch" aria-checked="${lanEnabled}" aria-label="Allow my local network" class="switch ${lanEnabled ? "on" : ""}" data-action="local-network-toggle"></button><span class="settings-subnets" data-local-subnets>${subnetChoices}</span>`)}
	  <p class="settings-note">Local access is limited to loopback and the configured model server; link-local, cloud metadata and Agent_b's own listener remain refused.</p>
	  ${row("actions", `<div class="settings-actions"><button type="button" data-action="setup-service-account" ${setupDisabled ? "disabled" : ""}>${setupLabel}</button></div>`)}
	${feedback(hardeningMessage, hardeningAlarm)}
	</details>
	${toggle("sandbox.enabled", "Docker Sandbox", sandboxEnabled, "Install-wide: routes shell and bash through Docker Sandbox.")}
	${row("Docker status", `<span class="account-status"><span class="lamp ${sandboxStatus.available ? "live" : ""}"></span>${html(sandboxState)}</span>`)}
    `;
}

// TRUSTED MANAGEMENT, in the one place the operator already trusts. A
// credential is typed here and nowhere else: not in a chat, not by the model, not from
// the phone. What is listed is the name, where it may go, how it is sent and when it was
// stored — never the value, which this page cannot read back either.
function credentialRows() {
  const entries = Array.isArray(credentialList) ? credentialList : [];
  const listed = entries.length ? `<table class="credential-table"><thead><tr><th>Name</th><th>Kind</th><th>Used for</th><th>State</th><th>Actions</th></tr></thead><tbody>${entries.map((entry) => {
    const signIn = entry.kind === "entra" || entry.kind === "google";
    const kind = signIn ? "sign-in" : entry.kind;
    const provider = entry.kind === "google" ? "Google" : "Microsoft";
    const origins = entry.microsoft ? "Graph · Azure · Exchange Online" : (entry.origins || [entry.origin]).join(" · ");
    const state = signIn ? entry.account ? "Signed in" : "Sign-in needed" : "Stored";
    const action = (verb, label) => `<button type="button" data-action="credential-${verb}" data-id="${attr(entry.name)}">${label}</button>`;
    const device = entry.kind === "entra" && credentialDevice.name === entry.name && credentialDevice.user_code
      ? `<a href="${attr(credentialDevice.verification_url)}" target="_blank" rel="noreferrer">${html(credentialDevice.user_code)}</a>` : "";
    return `<tr data-credential-name="${attr(entry.name)}"><td>${html(entry.name)}</td><td>${html(kind)}${signIn ? ` · ${provider}` : ""}</td><td>${html(origins)}</td><td title="${attr(entry.account || "")}">${state}</td><td><span class="settings-actions">${signIn ? action("sign-in", entry.account ? "Switch account" : "Sign in with browser") : ""}${entry.kind === "entra" ? action("device-code", "Use a device code") : ""}${entry.account ? action("sign-out", "Sign out") : ""}${action("remove", "Remove")}${device}</span></td></tr>`;
  }).join("")}</tbody></table>` : "";
  return `<section id="credential-settings">${subhead("Credentials")}
  ${toggle("credentials.ask_before_use", "ask before use", !!store.config.credentials?.ask_before_use, "The agent cannot use anything stored here until you approve it in the chat — once, or for that chat.")}
  ${store.config.credentials?.ask_before_use ? row("ask again after idle", `<input class="setting-input number" type="number" min="0" step="1" data-path="credentials.idle_hours" data-kind="number" value="${attr(store.config.credentials?.idle_hours ?? 4)}"><span class="control-note">hours · 0 = never</span>`) : ""}
  ${listed}
  ${credentialKind ? credentialForm() : '<button type="button" data-action="credential-open-add">Add</button>'}
  ${feedback(credentialMessage, credentialAlarm)}
  ${browserDataBytes == null ? "" : row("browser data", `<span class="settings-actions browser-data-actions">${browserDataConfirm ? '<span>Clear browser data?</span><button type="button" class="alarm" data-action="browser-data-clear">Clear</button><button type="button" data-action="browser-data-cancel">Cancel</button>' : browserDataBytes === 0 ? '<span class="account-status">empty</span>' : `<span class="mono account-status">${Math.ceil(browserDataBytes / 1048576)} MB</span><button type="button" data-action="browser-data-ask">Clear</button>`}</span>`, "", "Sign-ins, cookies and history the built-in browser keeps. Clear removes them.")}
  </section>`;
}

function credentialForm() {
  const input = (id, label, type = "text", value = "") => `<label>${label}<input id="${id}" type="${type}" autocomplete="${type === "password" ? "new-password" : "off"}" spellcheck="false" value="${attr(value)}"></label>`;
  const addresses = (id, placeholder) => `<label>Addresses<textarea rows="2" id="${id}" placeholder="${placeholder}" spellcheck="false"></textarea></label>`;
  const save = (action, words = "Save") => `<div class="settings-actions"><button type="button" data-action="${action}">${words}</button><button type="button" data-action="credential-cancel-add">Cancel</button></div>`;
  const advance = (content) => `<details class="settings-advanced" data-settings-advanced="credential" ${credentialAlarm ? "open" : ""}><summary>Advanced</summary>${content}</details>`;
  const option = (value, label) => `<option value="${value}"${credentialKind === value ? " selected" : ""}>${label}</option>`;
  let fields = "";
  if (credentialKind === "key") {
    fields = input("credential-name", "Name") + addresses("credential-origin", "https://api.example.test") + input("credential-secret", "Key", "password") + advance(input("credential-header", "Header (blank = bearer)")) + save("credential-add");
  } else if (credentialKind === "login") {
    fields = input("credential-login-name", "Name") + addresses("credential-login-origins", "user@host:22") + input("credential-login-password", "Password", "password") + save("credential-login-add");
  } else if (credentialKind === "sign-in") {
    const provider = `<label>Provider<select id="credential-provider"><option value="entra"${credentialProvider === "entra" ? " selected" : ""}>Microsoft</option><option value="google"${credentialProvider === "google" ? " selected" : ""}>Google</option></select></label>`;
    fields = credentialProvider === "entra" ? provider +
      input("credential-entra-name", "Name", "text", "microsoft") + input("credential-entra-origin", "Address", "text", "https://graph.microsoft.com") +
      advance(input("credential-tenant", "Tenant") + input("credential-client-id", "Client ID") + input("credential-scopes", "Scopes")) + save("credential-entra-add", "Sign in with browser")
      : provider + input("credential-google-name", "Name") + input("credential-google-origin", "Address") +
      input("credential-google-client-id", "Client ID") + input("credential-google-client-secret", "Client secret", "password") + input("credential-google-scopes", "Scopes") + save("credential-google-add");
  }
  return `<div class="credential-form"><label>Kind<select id="credential-kind">${option("choose", "Choose…")}${option("sign-in", "Sign-in")}${option("key", "Key")}${option("login", "Login")}</select></label>${fields || '<button type="button" data-action="credential-cancel-add">Cancel</button>'}</div>`;
}

// PAIRING AND STATUS WHERE THE OPERATOR LOOKS. Away from home the
// phone reaches this AgentB through the broker, and everything he needs for that is
// here: the address, the code to type into the phone, the fingerprint to compare on the
// two screens, the paired device, Revoke, and what the connection is doing right now.
//
// The broker carries ciphertext and cannot read any of it. The fingerprint is the whole
// of the user's part in that: if the two screens differ, the pairing is not his.
function brokerRows() {
	// the address is built into the build and is not in this page — no
	// field, no label, no read-out. The section is the pairing, the paired device and
	// Revoke; "off" is now a build without a broker, which the status says for itself.
	const status = brokerStatus || {};
	const offer = status.offer || {};
	const state = {
		"not paired": "NOT PAIRED",
		"broker unreachable": "PAIRED — BROKER UNREACHABLE",
		"holding": "PAIRED — HOLDING, PHONE NOT CONNECTED",
		"phone connected": "PAIRED — PHONE CONNECTED",
	}[status.state] || String(status.state || "NOT PAIRED").toUpperCase();
	const lamp = status.state === "phone connected" ? "live" : status.state === "broker unreachable" ? "alarm" : "";
	const connectionEvidence = [
		status.last_error ? html(status.last_error) : "",
		status.next_attempt_at ? `next try ${html(status.next_attempt_at)}` : "",
		status.last_message_at ? `last phone message ${html(status.last_message_at)}` : "",
		status.ended_reason ? `ended: ${html(status.ended_reason)}` : "",
	].filter(Boolean).join(" · ");
	const pairingLogHref = status.log_path ? encodeURI(`file:///${status.log_path.replaceAll("\\", "/")}`) : "";
	const paired = status.paired_device
		? `${html(status.paired_device)}<button type="button" data-action="broker-revoke" data-confirm="the paired phone">Revoke</button>`
		: "none paired";
	return `${subhead("Phone", "One phone, paired through the broker. It carries ciphertext and can read none of it; the fingerprint below is how you check that for yourself.")}
	${row("connection", `<span class="account-status"><span class="lamp ${lamp}"></span>${html(state)}${status.broker_build ? ` · build ${html(status.broker_build)}` : ""}${connectionEvidence ? `<small>${connectionEvidence}</small>` : ""}</span>`)}
	${row("pairing", offer.code
		// THE QR IS THE PAIRING DISPLAY. "this code is waaay too long its
		// insane" — the code is unchanged, because its length is its margin and three
		// repositories agree on it; what changed is that he never types it. The code
		// stays beneath in small plain text for a phone with no camera.
		// the square stays until the phone pairs or he presses Cancel, and
		// a code the broker expires is replaced under it without a blank moment. Cancel
		// is this row's own button, reading differently while a pairing is under way.
		? `<span class="pairing-offer">${offer.qr ? `<img class="pairing-qr" src="${attr(offer.qr)}" width="220" height="220" alt="Pairing QR code">` : ""}<span class="account-status mono pairing-code">${html(offer.code)}</span><button type="button" data-action="broker-cancel">Cancel</button></span>`
		: `<span class="account-status">${status.paired_device ? "paired" : "not paired"}</span><button type="button" data-action="broker-pair">Pair a phone</button>`,
		"", "Point the phone's camera at this and tap Pair. It works once.")}
	${offer.fingerprint ? row("fingerprint", `<span class="account-status mono">${html(offer.fingerprint)}</span><button type="button" data-action="broker-confirm">They match</button>`, "", "Compare all ten groups with the phone before confirming.") : ""}
	${row("device", `<span class="account-status">${paired}</span>`)}
	${status.log_path ? `<p class="account-status"><a href="${attr(pairingLogHref)}">open pairing log</a> · ${html(status.log_path)}</p>` : ""}
	${feedback(brokerMessage, brokerAlarm)}`;
}

function feedback(message, alarm, fallback) {
	const value = message || fallback || "";
	if (!value) return "";
	return `<p class="settings-feedback ${alarm ? "alarm" : ""}" role="status" title="${attr(value)}">${html(value)}</p>`;
}

function hardeningConnections() {
	const selected = selectedHardeningConnectionID();
	const options = connectionList()
		.filter((connection) => !connectionReason(connection))
		.map((connection) => `<option value="${attr(connection.id)}" ${connection.id === selected ? "selected" : ""}>${html(connection.label)} · ${html(connection.base_url)}</option>`)
		.join("");
	return options || '<option value="">No ready connection</option>';
}

function sessionControls(active) {
  if (!active) return '<p class="settings-note">No active session.</p>';
  const resetKey = `reset:${active.id}`;
  return `<div class="settings-actions vertical">
      <button type="button" data-action="reset-session" data-id="${attr(active.id)}" data-confirm="this conversation">Clear conversation</button>
    </div>

    ${copyRow("JSONL", active.log_path || "", "The file this chat's full record is written to; copy copies its path.")}`;
}


export function renderSecurityPage(page, active, context) {
  useSettingsContext(context);
  return page === "session" ? sessionControls(active) : shell(active);
}
import { actionTable } from "./settings-action-table.js";

export const securityActions = actionTable([
  "browser-data-ask", "browser-data-cancel", "browser-data-clear",
  "revoke-standing-grant", "trusted-folder-add", "trusted-folder-change",
  "trusted-folder-delete", "operator-context", "local-network-toggle",
  "open-setup", "session-tool-toggle", "store-shell-credential",
  "test-shell-credential", "clear-shell-credential", "setup-service-account",
  "service-identity-toggle", "disable-service-account", "refresh-service-account",
  "broker-pair", "broker-confirm", "broker-revoke", "broker-cancel",
  "credential-open-add", "credential-cancel-add", "credential-add", "credential-login-add", "credential-entra-add", "credential-google-add", "credential-sign-in",
  "credential-device-code", "credential-sign-out", "credential-remove",
  "apply-hardening", "verify-hardening", "refresh-hardening", "remove-hardening",
]);
