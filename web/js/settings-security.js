let serviceAccountLog = "";
let brokerStatus = {};
let brokerMessage = "";
let brokerAlarm = false;
let credentialList = [];
let credentialMessage = "";
let credentialAlarm = false;
let credentialDevice = {};
let store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, phoneAccess, standingGrants, connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView;
function useSettingsContext(context) {
  serviceAccountLog = context.serviceAccountLog || "";
  brokerStatus = context.brokerStatus || {};
  brokerMessage = context.brokerMessage || "";
  brokerAlarm = !!context.brokerAlarm;
  credentialList = context.credentialList || [];
  credentialMessage = context.credentialMessage || "";
  credentialAlarm = !!context.credentialAlarm;
  credentialDevice = context.credentialDevice || {};
  ({ store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, phoneAccess = { devices: [] }, standingGrants = [], connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView } = context);
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
							// Item 2np (c): the account EXISTS and the identity is off by
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
	const operatorView = operatorStatusView(store.shell_identity);
	const lanEnabled = !!store.config.shell?.allow_local_network;
	const sandboxEnabled = store.config.sandbox?.enabled !== false;
	const sandboxStatus = store.sandbox || {};
	const sandboxState = sandboxStatus.available ? "ready" : `inert · ${sandboxStatus.reason || "Docker Sandbox is unavailable"}`;
	const trustedFolders = Array.isArray(store.config.shell?.trusted_folders) ? store.config.shell.trusted_folders : [];
	const trustedRows = trustedFolders.map((entry, index) => `<span class="settings-actions"><input id="trusted-folder-${index}" value="${attr(entry.path)}" aria-label="Trusted folder"><span>${html((entry.added_at || "").slice(0, 10))} · ${entry.source === "card" ? "from a card" : "in Settings"}</span><button type="button" data-action="trusted-folder-change" data-id="${index}">Change</button><button type="button" data-action="trusted-folder-delete" data-id="${index}">Delete</button></span>`).join("");
	const confirmedSubnets = new Set(store.config.shell?.confirmed_local_subnets || []);
	const detectedSubnets = hardeningStatus.detected_local_subnets || [];
	const subnetChoices = detectedSubnets.length
		? detectedSubnets.map((subnet) => `<label class="settings-chip"><input type="checkbox" data-local-subnet value="${attr(subnet)}" ${confirmedSubnets.has(subnet) ? "checked" : ""} ${lanEnabled ? "" : "disabled"}> ${html(subnet)}</label>`).join("")
		: '<span class="settings-chip-empty">none detected</span>';
	// Item 2np (d): OFF IS NOT AN ERROR. The panel opened whenever the state was not
	// ready, which includes the identity being off by choice — so a deliberate setting
	// looked like a fault with a repair panel hanging open under it. It opens when
	// something is actually wrong with an identity that is on, or being turned on.
	const identityOn = !!service.enabled;
	const setupOpen = (identityOn || serviceAccountBusy) && serviceAccountStatus.state !== "ready";
	const identityAlarm = (identityOn || serviceAccountBusy) && serviceAccountStatus.loaded && serviceAccountStatus.state !== "ready";
  return `${subhead("Operator mode", "Run everything as you for 20 minutes. This is the one line that stays visible because misreading it is dangerous.")}
	${row("identity", `<button type="button" class="settings-operator-status" data-action="operator-context" aria-pressed="${operatorView.active}" aria-label="${attr(operatorView.label)}"><img src="${operatorView.src}" srcset="${operatorView.srcset}" width="24" height="24" alt=""><span>${operatorView.active ? "Stop running everything as me" : "Run everything as me for 20 minutes"}</span></button>`, "", "Runs every tool as you, without the service account's limits, for 20 minutes or until you stop it.")}
	<p class="settings-note">This defeats the service-account OS boundary for every tool in every chat until it expires.</p>
	${subhead("Unattended", "Never ask. A worker or scheduled run that would have raised a card records a boundary failure on its item and carries on; the report lists every one. This changes who is asked, not who runs anything and not what the boundary permits.")}
	${toggle("approval.unattended", "Unattended: never ask; boundary hits fail the item", !!store.config.approval?.unattended, "A chat you are typing in still asks you. This is for a worker started by Go and for a scheduled run, which have nobody in front of them.")}
	${subhead("Standing grants", "Exact repo, folder, connector, and host approvals for this profile. They survive restarts and new chats until revoked.")}
	${row("grants", standingGrants.length ? `<span class="settings-actions vertical">${standingGrants.map((grant)=>`<span>${html(grant.kind)} · ${html(grant.subject)} <button type="button" data-action="revoke-standing-grant" data-id="${attr(grant.id)}" data-confirm="the ${attr(grant.kind)} grant for ${attr(grant.subject)}">Revoke</button></span>`).join("")}</span>` : '<span class="account-status">none</span>')}
	${subhead("Docker Sandbox", "Install-wide: routes shell and bash through Docker Sandbox. When Docker Sandbox is unavailable the setting stays on but is inert and reports why.")}
	${toggle("sandbox.enabled", "Docker Sandbox", sandboxEnabled, "Install-wide: routes shell and bash through Docker Sandbox.")}
	${row("status", `<span class="account-status"><span class="lamp ${sandboxStatus.available ? "live" : ""}"></span>${html(sandboxState)}</span>`, "", "Inert means the setting is on but Docker Sandbox is unavailable; the reason is shown here.")}
	${subhead("Folders that don't ask", "A folder and its descendants skip outside-folder prompts; with Service identity on they run as you.")}
	${row("folders", `<span class="settings-actions vertical">${trustedRows || '<span class="account-status">none</span>'}<span class="settings-actions"><input id="trusted-folder-new" placeholder="C:\\folder" aria-label="Add trusted folder"><button type="button" data-action="trusted-folder-add">Add</button></span></span>`)}
	${subhead("Service identity", "Use the restricted Windows account for tools. Switching it on sets the account up and asks Windows once; switching it off takes effect immediately.")}
	${row("Service identity", `<button type="button" role="switch" aria-checked="${identityOn}" aria-label="Service identity" class="switch ${identityOn ? "on" : ""}" data-action="service-identity-toggle" ${serviceAccountBusy ? "disabled" : ""}></button><span class="account-status">${serviceAccountBusy ? "Turning on — Windows will ask once" : identityOn ? "on" : "off"}</span>`, "", "Off: tools run as the account that launched Agent_b. On: Agent_b runs tools as its own restricted Windows account.")}
	${feedback(serviceAccountMessage, serviceAccountAlarm)}
	${serviceAccountMessage && serviceAccountLog ? `<p class="account-status">${html(serviceAccountLog)}</p>` : ""}
	<details class="settings-advanced"${setupOpen ? " open" : ""}>
	  <summary>Set up service identity</summary>
	  <p class="settings-note">Agent_b generates and stores the password. One Windows approval creates or repairs the account, folder access and outbound policy, then tests the credential before enabling it.</p>
	  ${store.shell_identity?.notice ? `<p class="settings-feedback alarm" role="status">${html(store.shell_identity.notice)}</p>` : ""}
	  ${row("account", `<span class="account-status"><span class="lamp ${serviceAccountStatus.state === "ready" ? "live" : identityAlarm ? "alarm" : ""}"></span>${html(accountState)}</span>`)}
	  ${row("credential", `<span class="account-status">${html(stored)}</span>`)}
	  ${subhead("Host protections", "Folder access and outbound policy applied to the service identity.")}
	  ${row("protections", `<span class="account-status"><span class="lamp ${protectionReady ? "live" : hardeningStatus.loaded ? "alarm" : ""}"></span>${html(protectionState)}</span>`)}
	  ${driftLines ? `<ul class="settings-drift">${driftLines}</ul>` : ""}
	  ${row("model route", `<select id="hardening-connection" aria-label="Model route for host protections">${hardeningConnections()}</select>`)}
	  ${row("Allow my local network", `<button type="button" role="switch" aria-checked="${lanEnabled}" aria-label="Allow my local network" class="switch ${lanEnabled ? "on" : ""}" data-action="local-network-toggle"></button><span class="settings-subnets" data-local-subnets>${subnetChoices}</span>`)}
	  <p class="settings-note">Local access is limited to loopback and the configured model server; link-local, cloud metadata and Agent_b's own listener remain refused.</p>
	  ${row("actions", `<div class="settings-actions"><button type="button" data-action="setup-service-account" ${setupDisabled ? "disabled" : ""}>${setupLabel}</button></div>`)}
	${feedback(hardeningMessage, hardeningAlarm)}
	</details>
	${subhead("Phone access", "One-time enrolment and revocable phone sessions. The phone uses the same chat endpoints as this page.")}
	${row("enrolment", `<span class="account-status mono">${phoneAccess.code ? html(phoneAccess.code) : "no active code"}</span><button type="button" data-action="phone-enrol">New code</button>`, "", phoneAccess.expires_at ? `Expires ${phoneAccess.expires_at}` : "The code expires in five minutes and works once.")}
	${row("devices", phoneDevices())}
	${row("push", `<button type="button" role="switch" aria-checked="${!!phoneAccess.push_enabled}" class="switch ${phoneAccess.push_enabled ? "on" : ""}" data-action="phone-push-toggle"></button><span class="account-status">${phoneAccess.push_enabled ? "enabled" : "off"}</span>`, "", "Push carries only the notice line and chat name.")}
	${brokerRows()}
	${credentialRows()}
    `;
}

// Item 2nv (c): TRUSTED MANAGEMENT, in the one place the operator already trusts. A
// credential is typed here and nowhere else: not in a chat, not by the model, not from
// the phone. What is listed is the name, where it may go, how it is sent and when it was
// stored — never the value, which this page cannot read back either.
function credentialRows() {
	const entries = Array.isArray(credentialList) ? credentialList : [];
	const listed = entries.length
		? `<span class="settings-actions vertical">${entries.map((entry) => {
			const remove = `<button type="button" data-action="credential-remove" data-id="${attr(entry.name)}" data-confirm="the credential ${attr(entry.name)}">Remove</button>`;
			if (entry.kind !== "entra") return `<span>${html(entry.name)} · ${html(entry.origin)}${entry.header ? ` · ${html(entry.header)}` : " · bearer"} · stored ${html((entry.stored_at || "").slice(0, 10))} ${remove}</span>`;
			const account = entry.account ? `signed in as ${html(entry.account)}` : "sign-in needed";
			const device = credentialDevice.name === entry.name && credentialDevice.user_code
				? `<span class="account-status">Open <a href="${attr(credentialDevice.verification_url)}" target="_blank" rel="noreferrer">${html(credentialDevice.verification_url)}</a> and enter <span class="mono">${html(credentialDevice.user_code)}</span>.</span>` : "";
			return `<span class="settings-actions vertical"><span>${html(entry.name)} · Microsoft Entra · ${html(entry.origin)} · ${account}</span><span class="settings-actions"><button type="button" data-action="credential-sign-in" data-id="${attr(entry.name)}">${entry.account ? "Switch account" : "Sign in with browser"}</button><button type="button" data-action="credential-device-code" data-id="${attr(entry.name)}">Use a device code</button>${entry.account ? `<button type="button" data-action="credential-sign-out" data-id="${attr(entry.name)}">Sign out</button>` : ""}${remove}</span>${device}</span>`;
		}).join("")}</span>`
		: '<span class="account-status">none stored</span>';
	return `${subhead("Credentials", "Keys and signed-in accounts this machine holds for the services you connect to. Each one goes only to the address you approve for it — scheme, host and port — and to nothing else. A connector names a credential; it never carries its value.")}
	${row("stored", listed)}
	${row("add key", `<span class="settings-actions"><input id="credential-name" type="text" placeholder="name" autocomplete="off" spellcheck="false"><input id="credential-origin" type="text" placeholder="https://host:port" autocomplete="off" spellcheck="false"><input id="credential-header" type="text" placeholder="header (blank = bearer)" autocomplete="off" spellcheck="false"><input id="credential-secret" type="password" placeholder="the key" autocomplete="new-password" spellcheck="false"><button type="button" data-action="credential-add">Add</button></span>`, "", "The key is never shown again once stored, and never leaves this machine except to the address above. Windows protects it for your account: that is protection at rest and from other users, not from programs running as you.")}
	<details class="settings-advanced"><summary>Add Microsoft Entra</summary>
	${row("identity", `<span class="settings-actions"><input id="credential-entra-name" type="text" placeholder="name" autocomplete="off" spellcheck="false"><input id="credential-entra-origin" type="text" placeholder="https://API-host:port" autocomplete="off" spellcheck="false"></span>`)}
	${row("app registration", `<span class="settings-actions"><input id="credential-tenant" type="text" placeholder="tenant ID" autocomplete="off" spellcheck="false"><input id="credential-client-id" type="text" placeholder="client ID" autocomplete="off" spellcheck="false"></span>`)}
	${row("scopes", `<span class="settings-actions"><input id="credential-scopes" type="text" placeholder="space-separated scopes" autocomplete="off" spellcheck="false"><button type="button" data-action="credential-entra-add">Add Entra credential</button></span>`, "", "Use the API's public-client registration and delegated scopes. Agent_b supplies no tenant, client ID or scope defaults; sign-in is a separate explicit step.")}
	</details>
	${feedback(credentialMessage, credentialAlarm)}`;
}

// Item 2kq (b) and (e): PAIRING AND STATUS WHERE THE OPERATOR LOOKS. Away from home the
// phone reaches this AgentB through the broker, and everything he needs for that is
// here: the address, the code to type into the phone, the fingerprint to compare on the
// two screens, the paired device, Revoke, and what the connection is doing right now.
//
// The broker carries ciphertext and cannot read any of it. The fingerprint is the whole
// of the operator's part in that: if the two screens differ, the pairing is not his.
function brokerRows() {
	// Item 2nu (b): the address is built into the build and is not in this page — no
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
		status.next_attempt_at ? `next try ${html(status.next_attempt_at)}` : "",
		status.last_message_at ? `last phone message ${html(status.last_message_at)}` : "",
		status.ended_reason ? `ended: ${html(status.ended_reason)}` : "",
	].filter(Boolean).join(" · ");
	const paired = status.paired_device
		? `${html(status.paired_device)}<button type="button" data-action="broker-revoke" data-confirm="the paired phone">Revoke</button>`
		: "none paired";
	return `${subhead("Phone away from home", "One phone, paired through the broker. It carries ciphertext and can read none of it; the fingerprint below is how you check that for yourself.")}
	${row("connection", `<span class="account-status"><span class="lamp ${lamp}"></span>${html(state)}${status.broker_build ? ` · build ${html(status.broker_build)}` : ""}${connectionEvidence ? `<small>${connectionEvidence}</small>` : ""}</span>`)}
	${row("pairing", offer.code
		// Item 2ns (b): THE QR IS THE PAIRING DISPLAY. "this code is waaay too long its
		// insane" — the code is unchanged, because its length is its margin and three
		// repositories agree on it; what changed is that he never types it. The code
		// stays beneath in small plain text for a phone with no camera.
		// Item 2nz (a): the square stays until the phone pairs or he presses Cancel, and
		// a code the broker expires is replaced under it without a blank moment. Cancel
		// is this row's own button, reading differently while a pairing is under way.
		? `<span class="pairing-offer">${offer.qr ? `<img class="pairing-qr" src="${attr(offer.qr)}" width="220" height="220" alt="Pairing QR code">` : ""}<span class="account-status mono pairing-code">${html(offer.code)}</span><button type="button" data-action="broker-cancel">Cancel</button></span>`
		: `<span class="account-status">${status.paired_device ? "paired" : "not paired"}</span><button type="button" data-action="broker-pair">Pair a phone</button>`,
		"", "Point the phone's camera at this and tap Pair. It works once.")}
	${offer.fingerprint ? row("fingerprint", `<span class="account-status mono">${html(offer.fingerprint)}</span><button type="button" data-action="broker-confirm">They match</button>`, "", "Compare all ten groups with the phone before confirming.") : ""}
	${row("device", `<span class="account-status">${paired}</span>`)}
	${feedback(brokerMessage, brokerAlarm)}`;
}

function phoneDevices() {
	const devices = phoneAccess.devices || [];
	if (!devices.length) return '<span class="account-status">none enrolled</span>';
	return `<span class="settings-actions vertical">${devices.map((device) => `<span>${html(device.name)} · ${html(device.last_seen || "never")} <button type="button" data-action="phone-revoke" data-id="${attr(device.id)}">Revoke</button></span>`).join("")}<button type="button" data-action="phone-revoke-all">Revoke all</button></span>`;
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
