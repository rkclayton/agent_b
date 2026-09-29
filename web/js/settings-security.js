let serviceAccountLog = "";
let brokerStatus = {};
let brokerMessage = "";
let brokerAlarm = false;
let store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, phoneAccess, standingGrants, connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView;
function useSettingsContext(context) {
  serviceAccountLog = context.serviceAccountLog || "";
  brokerStatus = context.brokerStatus || {};
  brokerMessage = context.brokerMessage || "";
  brokerAlarm = !!context.brokerAlarm;
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
    `;
}

// Item 2kq (b) and (e): PAIRING AND STATUS WHERE THE OPERATOR LOOKS. Away from home the
// phone reaches this AgentB through the broker, and everything he needs for that is
// here: the address, the code to type into the phone, the fingerprint to compare on the
// two screens, the paired device, Revoke, and what the connection is doing right now.
//
// The broker carries ciphertext and cannot read any of it. The fingerprint is the whole
// of the operator's part in that: if the two screens differ, the pairing is not his.
function brokerRows() {
	const url = store.config.broker?.url || "";
	const status = brokerStatus || {};
	const offer = status.offer || {};
	const state = !url
		? "off — no broker address"
		: status.state || "not connected";
	const lamp = status.state === "connected" ? "live" : url && status.state === "reconnecting" ? "alarm" : "";
	const paired = status.paired_device
		? `${html(status.paired_device)}<button type="button" data-action="broker-revoke" data-confirm="the paired phone">Revoke</button>`
		: "none paired";
	return `${subhead("Phone away from home", "One phone, paired through the broker. It carries ciphertext and can read none of it; the fingerprint below is how you check that for yourself.")}
	${text("broker.url", "broker", url, "text", "The broker this AgentB dials out to. Empty means nothing dials.")}
	${row("connection", `<span class="account-status"><span class="lamp ${lamp}"></span>${html(state)}${status.broker_build ? ` · build ${html(status.broker_build)}` : ""}</span>`)}
	${row("pairing", offer.code
		? `<span class="account-status mono">${html(offer.code)}</span>`
		: `<span class="account-status">${status.paired_device ? "paired" : "not paired"}</span><button type="button" data-action="broker-pair" ${url ? "" : "disabled"}>Pair a phone</button>`,
		"", "Type this code into the phone. It expires in ten minutes and works once.")}
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
