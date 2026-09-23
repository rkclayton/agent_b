let store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, signingStatus, signingBusy, signingMessage, signingAlarm, serverProfiles, row, subhead, text, toggle, copyRow, profileReason, html, attr, selectedHardeningServerID, operatorStatusView;
function useSettingsContext(context) {
  ({ store, armed, drafts, shellCredentialMessage, shellCredentialAlarm, serviceAccountStatus, serviceAccountBusy, serviceAccountMessage, serviceAccountAlarm, hardeningStatus, hardeningBusy, hardeningMessage, hardeningAlarm, signingStatus, signingBusy, signingMessage, signingAlarm, serverProfiles, row, subhead, text, toggle, copyRow, profileReason, html, attr, selectedHardeningServerID, operatorStatusView } = context);
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
			: serviceAccountStatus.exists
				? `${service.account || "agentb-svc"} · ${serviceAccountStatus.enabled ? "enabled" : "disabled"}${serviceAccountStatus.administrator ? " · ADMINISTRATOR — refused" : !serviceAccountStatus.users_member ? " · Users membership missing" : " · non-admin"}`
				: "not created";
	const setupAction = serviceAccountStatus.exists ? "reset" : "create";
	const setupLabel = serviceAccountBusy
		? "Waiting for Windows UAC…"
		: service.enabled
			? "Reset password and test"
			: "Turn on and test";
	const setupDisabled = serviceAccountBusy || !serviceAccountStatus.loaded || !serviceAccountStatus.supported || serviceAccountStatus.administrator;
	const profile = serverProfiles().find((item) => item.id === selectedHardeningServerID());
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
							: !profile
								? "Test and select a runnable model connection first."
								: "";
	const canApply = !hardeningBusy && !applyBlocker;
	const canInspect = !hardeningBusy && hardeningStatus.loaded && hardeningStatus.supported && serviceAccountStatus.exists;
	const canTestIdentity = !serviceAccountBusy && credential.stored && protectionReady;
	const protectionFeedback = applyBlocker
		? `Apply unavailable: ${applyBlocker}${hardeningMessage ? ` Last result: ${hardeningMessage}` : ""}`
		: hardeningMessage;
	const signedFiles = signingStatus.files || [];
	const signaturesValid = signedFiles.length > 0 && signedFiles.every((file) => file.status === "Valid" && file.thumbprint === signingStatus.thumbprint && file.timestamped);
	const certificateDone = signingStatus.configured && signingStatus.has_private_key && signingStatus.code_signing_eku;
	const verifyDone = certificateDone && signingStatus.chain_valid && signaturesValid;
	const signingAllowed = signingStatus.can_manage && !signingBusy;
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
  return `${subhead("Operator mode", "Run everything as you for 20 minutes. This is the one line that stays visible because misreading it is dangerous.")}
	${row("identity", `<button type="button" class="settings-operator-status" data-action="operator-context" aria-pressed="${operatorView.active}" aria-label="${attr(operatorView.label)}"><img src="${operatorView.src}" srcset="${operatorView.srcset}" width="24" height="24" alt=""><span>${operatorView.active ? "Stop running everything as me" : "Run everything as me for 20 minutes"}</span></button>`, "", "Runs every tool as you, without the service account's limits, for 20 minutes or until you stop it.")}
	<p class="settings-note">This defeats the service-account OS boundary for every tool in every chat until it expires.</p>
	${subhead("Unattended", "Never ask. A worker or scheduled run that would have raised a card records a boundary failure on its item and carries on; the report lists every one. This changes who is asked, not who runs anything and not what the boundary permits.")}
	${toggle("approval.unattended", "Unattended: never ask; boundary hits fail the item", !!store.config.approval?.unattended, "A chat you are typing in still asks you. This is for a worker started by Go and for a scheduled run, which have nobody in front of them.")}
	${subhead("Docker Sandbox", "Install-wide: routes shell and bash through Docker Sandbox. When Docker Sandbox is unavailable the setting stays on but is inert and reports why.")}
	${toggle("sandbox.enabled", "Docker Sandbox", sandboxEnabled, "Install-wide: routes shell and bash through Docker Sandbox.")}
	${row("status", `<span class="account-status"><span class="lamp ${sandboxStatus.available ? "live" : ""}"></span>${html(sandboxState)}</span>`, "", "Inert means the setting is on but Docker Sandbox is unavailable; the reason is shown here.")}
	${subhead("Service identity", "The non-admin Windows account shell and file tools run as. Windows may request approval.")}
	<p class="settings-note">The service identity limits what tool processes can reach; turning it on asks Windows to provision the account, then tests the credential before enabling it.</p>
	${store.shell_identity?.notice ? `<p class="settings-feedback alarm" role="status">${html(store.shell_identity.notice)}</p>` : ""}
    ${row("status", `<span class="account-status"><span class="lamp ${serviceAccountStatus.administrator ? "alarm" : serviceAccountStatus.exists ? "live" : ""}"></span>${html(accountState)}</span>`, "", "Whether the low-privilege Windows account Agent_b runs tools as exists, and that it is not an administrator.")}
    ${row("credential", `<span class="account-status">${html(stored)}</span>`, "", "Whether that account's password is stored for Agent_b, encrypted for this Windows user.")}
    ${row("new password", `<input id="service-account-setup-password" type="password" autocomplete="new-password" aria-label="New service-account password" ${setupDisabled ? "disabled" : ""}>`, "", "The password to create the service account with.")}
    ${row("repeat", `<input id="service-account-setup-confirmation" type="password" autocomplete="new-password" aria-label="Repeat new service-account password" ${setupDisabled ? "disabled" : ""}>`, "", "The same password again, to catch a typo.")}
    <div class="settings-actions">
      <button type="button" data-action="setup-service-account" data-setup-action="${setupAction}" ${setupDisabled ? "disabled" : ""}>${setupLabel}</button>
	  ${service.enabled ? '<button type="button" data-action="disable-service-account">Turn off service identity</button>' : ""}
      <button type="button" data-action="test-shell-credential" title="${protectionReady ? "" : "Apply host protection before testing folder access."}" ${canTestIdentity ? "" : "disabled"}>Test identity</button>
      <button type="button" data-action="refresh-service-account" ${serviceAccountBusy ? "disabled" : ""}>Refresh</button>
    </div>
    ${feedback(serviceAccountMessage, serviceAccountAlarm, "The non-admin Windows account used by shell and file tools. Windows may request approval.")}
	${subhead("Host protections", "Applies folder access for the service identity and the user-scoped outbound firewall rule. Windows requests approval.")}
	${row("Agent_b", `<span class="account-status ${hardeningStatus.harness_elevated ? "alarm" : ""}">${html(elevationState)}</span>`, "", "Whether Agent_b itself is running elevated; it should not be.")}
	${row("status", `<span class="account-status"><span class="lamp ${protectionReady ? "live" : hardeningStatus.loaded ? "alarm" : ""}"></span>${html(protectionState)}</span>`, "", "Whether the folder permissions and the outbound firewall rule that confine the service account are in place.")}
	${row("model route", `<select id="hardening-server" aria-label="Model route for host protections">${hardeningProfiles()}</select>`, "", "The endpoint the firewall rule lets the service account reach.")}
	<p class="settings-note">The network boundary allows loopback and the configured model server${(store.config.shell?.allowed_model_ranges || []).length ? `, plus configured ranges ${html(store.config.shell.allowed_model_ranges.join(", "))}` : ""}.</p>
	${row("Allow my local network", `<button type="button" role="switch" aria-checked="${lanEnabled}" aria-label="Allow my local network" class="switch ${lanEnabled ? "on" : ""}" data-action="local-network-toggle"></button><span class="settings-subnets" data-local-subnets>${subnetChoices}</span>`, "", "Select each detected subnet you intend to expose, then Apply protection. Link-local, cloud metadata and Agent_b's own listener remain refused.")}
	<div class="settings-actions">
	  <button type="button" data-action="apply-hardening" title="${attr(applyBlocker)}" aria-busy="${hardeningBusy}" ${canApply ? "" : "disabled"}>${hardeningBusy ? "Working…" : drafts.size ? "Save first" : "Apply protection"}</button>
	  <button type="button" data-action="verify-hardening" ${canInspect ? "" : "disabled"}>Verify</button>
	  <button type="button" data-action="refresh-hardening">Refresh</button>
	  <button type="button" class="${armed.has("hardening:remove") ? "confirm" : ""}" data-action="remove-hardening" ${canInspect ? "" : "disabled"}>${armed.has("hardening:remove") ? "Confirm remove" : "Remove"}</button>
	</div>
	${feedback(protectionFeedback, hardeningAlarm || !!applyBlocker, "Apply protection requests Windows approval, grants folder access, then tests the service identity.")}
	${subhead("Code signing", "Gives this installation a stable publisher identity and trusted local chain. It does not create Defender cloud reputation.")}
	${row("certificate", `<span class="account-status"><span class="lamp ${certificateDone ? "live" : ""}"></span>${html(certificateDone ? `${signingStatus.subject} · ${signingStatus.thumbprint}` : "not done")}</span>`, "", "The code-signing certificate Agent_b's files are signed with.")}
	${row("account", `<span class="account-status"><span class="lamp ${signingStatus.admin_state === "elevated" ? "live" : signingStatus.admin_state === "not_admin" ? "alarm" : ""}"></span>${html(adminStateText(signingStatus.admin_state))}</span>`, "", "Whether this Windows account can manage signing, and whether it needs an elevated run first.")}
	${row("artifacts", `<span class="account-status"><span class="lamp ${verifyDone ? "live" : signingStatus.loaded ? "alarm" : ""}"></span>${html(verifyDone ? "done · signed, timestamped, chain valid" : "not done")}</span>`, "", "Whether the installed files carry a valid, timestamped signature.")}
	${signingStatus.can_manage ? `<div class="settings-actions vertical">
	  <button type="button" data-action="create-signing" title="Create a protected certificate here, or import or select one you already own." ${signingAllowed ? "" : "disabled"}>Create certificate</button>
	  ${row("PFX", '<input id="signing-pfx" type="file" accept=".pfx,application/x-pkcs12">', "", "A certificate file with its private key, to import for signing.")}
	  ${row("password", '<input id="signing-password" type="password" autocomplete="off">', "", "The PFX file's password.")}
	  ${row("stored certificate", `<select id="signing-thumbprint"><option value="">Select code-signing certificate</option>${(signingStatus.certificates || []).filter((certificate) => certificate.has_private_key).map((certificate) => `<option value="${attr(certificate.thumbprint)}" ${certificate.thumbprint === store.config.signing?.thumbprint ? "selected" : ""}>${html(certificate.subject)} · ${html(certificate.thumbprint)}</option>`).join("")}</select>`, "", "A code-signing certificate already in the Windows store, to sign with.")}
	  <div class="settings-actions"><button type="button" data-action="import-signing" ${signingAllowed ? "" : "disabled"}>Import certificate</button><button type="button" data-action="export-signing" ${certificateDone && signingAllowed ? "" : "disabled"}>Export .cer</button></div>
	  <button type="button" data-action="sign-application" title="Sign Agent_b.exe and the PowerShell scripts, then restart Agent_b." ${certificateDone && signingAllowed ? "" : "disabled"}>Sign application</button>
	  <button type="button" data-action="verify-signing" title="Verify signer, thumbprint, timestamp and certificate chain." ${signingBusy ? "disabled" : ""}>Verify signatures</button>
	</div>` : `<div class="settings-actions vertical"><button type="button" data-action="verify-signing" title="Standard users can verify signatures but cannot create, import, select, export or sign." ${signingBusy ? "disabled" : ""}>Verify signatures</button></div>`}
	${feedback(signingMessage, signingAlarm, "Self-created keys are non-exportable and usable only by the elevated signing helper; imported keys keep their existing protection.")}
    <details class="settings-advanced">
      <summary>Advanced</summary>
      ${text("shell.command", "shell command", (store.config.shell?.command || []).join(" "), "command", "The program and arguments the shell tool runs commands with.")}
      ${text("shell.service_account.account", "account", service.account || "agentb-svc", "text", "The service account's user name.")}
      ${text("shell.service_account.domain", "domain", service.domain || ".", "text", "The service account's domain; a dot means this computer.")}
      ${row("store credential", '<input id="shell-service-password" type="password" autocomplete="new-password" aria-label="Service-account credential">', "", "The service account's password, stored encrypted for this Windows user.")}
      <div class="settings-actions">
        <button type="button" data-action="store-shell-credential" ${serviceAccountBusy ? "disabled" : ""}>Store credential</button>
        <button type="button" data-action="clear-shell-credential" ${serviceAccountBusy ? "disabled" : ""}>Clear credential</button>
      </div>
      ${feedback(shellCredentialMessage, shellCredentialAlarm, "Credentials are encrypted for this Windows user and never returned to the browser.")}
    </details>`;
}

function feedback(message, alarm, fallback) {
	const value = message || fallback || "";
	return `<p class="settings-feedback ${alarm ? "alarm" : ""}" role="status" title="${attr(value)}">${html(value)}</p>`;
}

function hardeningProfiles() {
	const selected = selectedHardeningServerID();
	const options = serverProfiles()
		.filter((profile) => !profileReason(profile))
		.map((profile) => `<option value="${attr(profile.id)}" ${profile.id === selected ? "selected" : ""}>${html(profile.label)} · ${html(profile.base_url)}</option>`)
		.join("");
	return options || '<option value="">No ready connection</option>';
}

function sessionControls(active) {
  if (!active) return '<p class="settings-note">No active session.</p>';
  const resetKey = `reset:${active.id}`;
  return `<div class="settings-actions vertical">
      <button type="button" class="${armed.has(resetKey) ? "confirm" : ""}" data-action="reset-session" data-id="${attr(active.id)}">${armed.has(resetKey) ? "Confirm clear" : "Clear conversation"}</button>
    </div>

    ${copyRow("JSONL", active.log_path || "", "The file this chat's full record is written to; copy copies its path.")}`;
}


// Item 2gj: three token states, three readouts. An unelevated administrator
// is told what to do, never refused; only a non-administrator is refused.
function adminStateText(state) {
	if (state === "elevated") return "administrator · elevated";
	if (state === "not_elevated") return "administrator · signing needs an elevated run";
	if (state === "not_admin") return "standard account · signing cannot be managed here";
	return "administrator status unknown until elevated";
}
export function renderSecurityPage(page, active, context) {
  useSettingsContext(context);
  return page === "session" ? sessionControls(active) : shell(active);
}
