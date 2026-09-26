let store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, phoneAccess, standingGrants, connectionList, row, subhead, text, toggle, copyRow, connectionReason, html, attr, selectedHardeningConnectionID, operatorStatusView;
function useSettingsContext(context) {
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
							: "agentb-svc · account not created";
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
	const setupOpen = serviceAccountStatus.state !== "ready";
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
	${subhead("Service identity", "Use the restricted Windows account for tools. Turning this off restores direct non-elevated operator execution after Save.")}
	${toggle("shell.service_account.enabled", "Service identity", !!service.enabled, "Off: tools run as the account that launched Agent_b. On: unavailable identity actions refuse and offer Repair or Run as you.")}
	<details class="settings-advanced"${setupOpen ? " open" : ""}>
	  <summary>Set up service identity</summary>
	  <p class="settings-note">Agent_b generates and stores the password. One Windows approval creates or repairs the account, folder access and outbound policy, then tests the credential before enabling it.</p>
	  ${store.shell_identity?.notice ? `<p class="settings-feedback alarm" role="status">${html(store.shell_identity.notice)}</p>` : ""}
	  ${row("account", `<span class="account-status"><span class="lamp ${serviceAccountStatus.state === "ready" ? "live" : serviceAccountStatus.loaded ? "alarm" : ""}"></span>${html(accountState)}</span>`)}
	  ${row("credential", `<span class="account-status">${html(stored)}</span>`)}
	  ${subhead("Host protections", "Folder access and outbound policy applied to the service identity.")}
	  ${row("protections", `<span class="account-status"><span class="lamp ${protectionReady ? "live" : hardeningStatus.loaded ? "alarm" : ""}"></span>${html(protectionState)}</span>`)}
	  ${driftLines ? `<ul class="settings-drift">${driftLines}</ul>` : ""}
	  ${row("model route", `<select id="hardening-connection" aria-label="Model route for host protections">${hardeningConnections()}</select>`)}
	  ${row("Allow my local network", `<button type="button" role="switch" aria-checked="${lanEnabled}" aria-label="Allow my local network" class="switch ${lanEnabled ? "on" : ""}" data-action="local-network-toggle"></button><span class="settings-subnets" data-local-subnets>${subnetChoices}</span>`)}
	  <p class="settings-note">Local access is limited to loopback and the configured model server; link-local, cloud metadata and Agent_b's own listener remain refused.</p>
	  ${row("actions", `<div class="settings-actions"><button type="button" data-action="setup-service-account" ${setupDisabled ? "disabled" : ""}>${setupLabel}</button></div>`)}
	${feedback(serviceAccountMessage || hardeningMessage, serviceAccountAlarm || hardeningAlarm)}
	</details>
	${subhead("Phone access", "One-time enrolment and revocable phone sessions. The phone uses the same chat endpoints as this page.")}
	${row("enrolment", `<span class="account-status mono">${phoneAccess.code ? html(phoneAccess.code) : "no active code"}</span><button type="button" data-action="phone-enrol">New code</button>`, "", phoneAccess.expires_at ? `Expires ${phoneAccess.expires_at}` : "The code expires in five minutes and works once.")}
	${row("devices", phoneDevices())}
	${row("push", `<button type="button" role="switch" aria-checked="${!!phoneAccess.push_enabled}" class="switch ${phoneAccess.push_enabled ? "on" : ""}" data-action="phone-push-toggle"></button><span class="account-status">${phoneAccess.push_enabled ? "enabled" : "off"}</span>`, "", "Push carries only the notice line and chat name.")}
    `;
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
