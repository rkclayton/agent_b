let store, armed, workspaceState, operatorFileState, row, subhead, number, toggle, currentValue, html, attr;
function useSettingsContext(context) {
  ({ store, armed, workspaceState, operatorFileState, row, subhead, number, toggle, currentValue, html, attr } = context);
}

function folders() {
	const directories = workspaceState.length ? workspaceState.map((item) => {
		const policyKey=`policy:${item.dir}`; const policy=item.policy;
		return `<div class="session-row workspace-row"><span class="path" title="${attr(item.dir)}">${html(item.dir)}</span><span>${item.memory_count} memory ${item.memory_count===1?"entry":"entries"}</span><span>${html(relativeDate(item.last_used))}</span></div>
		${policy ? `<div class="session-row workspace-policy-row"><span class="path" title="${attr(policy.path)}">${html(policy.path)}</span><code title="${attr(policy.hash)}">${html((policy.hash||"").slice(0,12))}</code><span>${html(policy.approved_at||"not approved")}</span><button type="button" data-action="revoke-workspace-policy" data-id="${attr(item.dir)}" data-confirm="the approval for ${attr(policy.path)}" ${policy.approved?"":"disabled"}>Revoke</button></div>`:""}`;
	}).join("") : '<p class="settings-note">No folders yet — a plan repo appears here when you add one</p>';
	return `${operatorFilesFolder()}${subhead("Known folders", "Plan repositories and folders that carry memory or an approved policy.")}${directories}`;
}

function operatorFilesFolder() {
	const bytes=Number(operatorFileState.attachment_bytes||0).toLocaleString("en-US");
	const files=Number(operatorFileState.attachment_files||0);
	const emptyKey="operator-attachments:empty";
	const dir=store.sessions[store.active]?.workspace||store.config.workspace||"";
	const found=operatorFileState.instruction_found||[];
	const adoptable=!found.includes("AGENT_B.md")&&found.some((name)=>name==="AGENTS.md"||name==="CLAUDE.md");
	const adopt=adoptable?`${subhead("Adopt repository instructions", `Create AGENT_B.md from ${found.join(" + ")}; source files remain in place.`)}
		<label class="settings-check warning" title="Cleanup is destructive and is off by default."><input id="adopt-instruction-cleanup" type="checkbox"> Also remove AGENTS.md / CLAUDE.md</label>
		<button type="button" data-action="adopt-instructions" data-id="${attr(dir)}">Adopt</button>`:"";
	return `${row("attachments",`<span class="path" title="${attr(operatorFileState.attachments_path||"")}">${files} files · ${bytes} bytes</span><button type="button" data-action="empty-operator-attachments" data-confirm="${attr(files)} attached files" ${files?"":"disabled"}>Empty</button>`,"","Files attached to chats, kept in the attachments folder; Empty removes them.")}
		${toggle("operator_files.allow_mailbox_approvals","Allow approvals from the mailbox",store.config.operator_files?.allow_mailbox_approvals===true,"Whoever can write to your synced folder can then grant the agent your identity.")}
		${number("operator_files.log_retention_days","log retention (days)",store.config.operator_files?.log_retention_days||30,"1",false,"",false,"number","How many days operational logs are kept; retained chats are not affected.")}
		${adopt}`;
}

function relativeDate(value) { if(!value)return "never"; const date=new Date(value); return Number.isNaN(date.valueOf())?value:date.toLocaleString(); }


export function renderWorkspacePage(context) {
  useSettingsContext(context);
  return folders();
}
