import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const deliverables = await readFile(new URL("./deliverables.js", import.meta.url), "utf8");
const app = await readFile(new URL("./app.js", import.meta.url), "utf8");
const css = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const shell = await readFile(new URL("./shell.js", import.meta.url), "utf8");
const bus = await readFile(new URL("./bus.js", import.meta.url), "utf8");
const state = await readFile(new URL("./state.js", import.meta.url), "utf8");
const tokens = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
const settings = (await Promise.all([
  "settings.js", "settings-chats.js", "settings-connections.js", "settings-general.js", "settings-context.js", "settings-run.js",
  "settings-about.js", "settings-workspace.js", "settings-security.js",
].map((name) => readFile(new URL(`./${name}`, import.meta.url), "utf8")))).join("\n");
// Item 2no: the Plan is a Settings section, so its markup is in the one document
// beside the panels Settings adopts. There is no plan.html any more.
const plan = html;
const consoleHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");
const pageSources = { app, chat, shell, settings, plan: await readFile(new URL("./plan.js", import.meta.url), "utf8"), timeline: await readFile(new URL("./timeline.js", import.meta.url), "utf8"), proposals: await readFile(new URL("./chat-proposals.js", import.meta.url), "utf8") };
const serverFiles = ["server_actions.go", "server_sessions.go", "server_connections.go", "agent_connection.go", "agents.go", "attachments.go", "chats.go", "chat_mirror.go", "credentials_endpoint.go", "files.go", "hardening.go", "measure.go", "notifications.go", "operator_files.go", "plan.go", "plan_page.go", "profiles.go", "service_account.go", "skills.go", "telemetry.go", "update.go", "worker.go", "broker_endpoint.go"];
const serverSources = Object.fromEntries(await Promise.all(serverFiles.map(async (name) => [name, await readFile(new URL(`../../internal/web/${name}`, import.meta.url), "utf8")])));

test("every page request body stays inside its route contract", () => {
  const rows = [
    "sessions/:id/messages/drop-last|||app|server_sessions.go", "stop|session_id|session_id|app,chat|server_actions.go", "sessions/:id/result-label|label,run_id|label,run_id|app|server_sessions.go",
    "agents|action,id,source_id,name,connection_id,model,prompt,tools,private|action,id,source_id,name,connection_id,model,prompt,tools,private|app|agents.go", "agents/:id/connection|action,role,connection_id|action,role,connection_id|app|agent_connection.go",
    "tools/:id|agent_id,enabled|agent_id,enabled|app,settings|server_actions.go", "stats/:id/clear|confirm|confirm|app|server_actions.go", "agents/:id/memory/flush|workspace,confirm|workspace,confirm|app|server_actions.go",
    "approve|session_id,call_id,decision|session_id,call_id,decision|app,chat|server_actions.go", "plan/accept|session_id,proposal|session_id,proposal|proposals|plan.go", "sessions/:id/rebind|||chat|server_sessions.go",
    "open-folder|session_id,path,scope|session_id,path,scope|chat,timeline|files.go", "open-file|session_id,path,scope|session_id,path,scope|chat|files.go", "service-account|action,connection_id,allow_local_network,local_subnets|action,connection_id,allow_local_network,local_subnets|chat,settings|service_account.go",
    "update|action,session_id|action,session_id|chat,settings|update.go", "message|session_id,text,attachments|session_id,text,attachments|chat|server_actions.go", "attachments/stop|upload_id|upload_id|chat|attachments.go", "speech/stop|||chat|server_actions.go",
    "connections/:id/probe||base_url,model,api_key,request_timeout_s|chat|server_connections.go", "connections/:id/probe|base_url,model,api_key,request_timeout_s|base_url,model,api_key,request_timeout_s|settings|server_connections.go", "connections/:id/models|base_url,api_key|base_url,api_key|settings|server_connections.go",
    "chat-mirror/take|chat_id|chat_id|chat|chat_mirror.go", "workspaces/:action|dir,hash,session_id|dir,hash,session_id,confirm|chat|server_sessions.go", "chats/delete-all|confirm|confirm|settings|chats.go", "connections/:id/key|||settings|server_connections.go",
    "config|shell,connections|shell,connections|chat,settings|server_connections.go",
    "profiles|action,name,new_name|action,name,new_name|settings|profiles.go", "skills|action,name,enabled,path,include|action,name,enabled,path,include|settings|skills.go", "agent-memory/remove|agent_id,note,confirm|agent_id,note,confirm|settings|server_sessions.go",
    "standing-grants|id|id|settings|server_actions.go", "workspaces/policy-revoke|dir|dir,hash,session_id,confirm|settings|server_sessions.go", "operator-files|action,confirm,dir,cleanup,confirm_cleanup|action,confirm,dir,cleanup,confirm_cleanup|settings|operator_files.go",
    "eval/measure|connection_id|connection_id|settings|measure.go", "connections/:id/recommended|||settings|server_connections.go", "broker|action|action|settings|broker_endpoint.go", "credentials|action,name,origin,header,secret,tenant,client_id,scopes|action,name,origin,header,secret,tenant,client_id,scopes|settings|credentials_endpoint.go",
    "sign-in-start|enabled|enabled|settings|server_actions.go", "hardening|action,connection_id,allow_local_network,local_subnets|action,connection_id,allow_local_network,local_subnets|settings|hardening.go", "notifications|action,url|action,url|settings|notifications.go",
    "shell-credential|action,password|action,password|settings|server_connections.go", "sessions/:id|label,agent_id,connection_id|label,agent_id,connection_id|shell,settings|server_sessions.go", "connections/:id/duplicate|id,label|id,label|settings|server_connections.go",
    "sessions|label,connection_id,agent_id,role,plan_id|label,connection_id,agent_id,source_session_id,role,plan_id|shell,settings|server_sessions.go", "sessions/:id/reopen|||shell,chat|server_sessions.go", "sessions/:id/reset|||settings|server_sessions.go",
    "host-window|action,title|action,title|shell|server_actions.go", "chats/tree|action,parent,name,path,id,folder,pinned|action,parent,name,path,id,folder,pinned|shell|chats.go", "header-state|state,reason_code|state,reason_code|shell|telemetry.go",
    "plan/go|plan_id,stop,auto_continue|session_id,plan_id,stop,auto_continue|plan|worker.go", "plans|repo|repo|plan|plan_page.go", "plans/build|plan_id,agent_id,brief|plan_id,agent_id,brief|plan|plan_page.go",
  ].map((row) => row.split("|"));
  for (const [route, sentCSV, acceptedCSV, pagesCSV, serverFile] of rows) {
    const sent = sentCSV ? sentCSV.split(",") : [], accepted = new Set(acceptedCSV ? acceptedCSV.split(",") : []);
    assert.deepEqual(sent.filter((field) => !accepted.has(field)), [], route);
    const pageText = pagesCSV.split(",").map((name) => pageSources[name]).join("\n").replaceAll("_", "").toLowerCase();
    const serverText = serverSources[serverFile].replaceAll("_", "").toLowerCase();
    for (const field of sent) assert.ok(pageText.includes(field.replaceAll("_", "")), `${route} page ${field}`);
    for (const field of accepted) assert.ok(serverText.includes(field.replaceAll("_", "")), `${route} server ${field}`);
  }
});

test("Chat has fence-only copy and documents composer keys", () => {
  assert.doesNotMatch(chat, /Copy message|messageCopy|assistantCopyText/);
  assert.match(html, /title="Send · Enter sends · Shift\+Enter newline"/);
});

test("sending a retained chat is one message request 2rd", () => {
  assert.doesNotMatch(chat, /session\.closed\)[^;]+\/reopen/);
  assert.match(chat, /api\("\/api\/message", \{ session_id: session\.id, text, attachments:/);
});

test("compaction states what was stubbed and marks file-write calls 2tv", () => {
	assert.match(chat, /older tool output and file-write bodies stubbed/);
	assert.match(state, /tool_calls\?\.some\(\(call\) => call\.arguments\?\.includes\("\[elided:"\)\)/);
	assert.match(state, /stubbed \? "elided"/);
});

test("Jump to latest is one accessible down-arrow at the transcript edge", () => {
  assert.match(chat, /jumpButton\.textContent = "↓";\s*jumpButton\.ariaLabel = "Jump to latest";\s*jumpButton\.title = "Jump to latest";/);
  assert.match(css, /\.chat-jump\s*\{[^}]*float:\s*right[^}]*bottom:\s*8px[^}]*width:\s*32px[^}]*min-height:\s*32px[^}]*padding:\s*0/s);
  assert.match(chat, /follow = log\.scrollHeight - log\.clientHeight - log\.scrollTop <= 24/);
  assert.match(chat, /jumpButton\.onclick = \(\) => \{\s*follow = true;[\s\S]*history_end[\s\S]*loadSessionHistory\(session\.id, false\);[\s\S]*else renderLog\(session\)/);
  assert.doesNotMatch(chat, /pointerover[\s\S]{0,180}follow = false/);
});

test("live-only status patches replace state without advancing the journal cursor 2qf", () => {
  assert.match(bus, /!patch\.transient[\s\S]*patch\.cursor/);
  assert.match(bus, /if \(!patch\.transient\) target\.cursor = patch\.cursor/);
});

test("queued drafts are cached before a removal patch applies", () => {
  assert.match(chat, /subscribeBeforePatch\(\(state, event\) => cacheQueuedDraft\(state\.sessions\[event\.data\?\.session_id\]\)\)/);
  assert.match(bus, /for \(const fn of beforePatchListeners\) fn\(store, \{ type: "projection\.patch", data: patch \}\)/);
});

test("whole-run projection replacements redraw chat-list state", () => assert.match(shell, /run\(\?:\\\/status\)\?/));

test("fenced code is one aligned panel with a header row and internal overflow", () => {
  assert.match(css, /\.code-block\s*\{[^}]*display:\s*grid[^}]*grid-template-areas:\s*"language copy"\s*"code code"[^}]*background:\s*var\(--bezel\)[^}]*border:\s*1px solid rgba\(112,\s*125,\s*139,\s*\.28\)[^}]*border-radius:\s*2px/s);
  assert.match(css, /\.chat-content \.code-block pre\s*\{[^}]*grid-area:\s*code[^}]*margin:\s*0[^}]*overflow-x:\s*auto[^}]*white-space:\s*pre/s);
  assert.match(css, /\.code-language\s*\{[^}]*grid-area:\s*language[^}]*padding:\s*4px 8px/s);
  assert.match(css, /\.chat-page > :where\(:not\(\.app-shell\)\) \.code-copy\s*\{[^}]*grid-area:\s*copy[^}]*position:\s*static/s);
});

test("Chat selection excludes chrome while preserving message content", () => {
  for (const selector of [".thinking-line", ".tool-tick", ".chat-notice-row", ".chat-jump"]) {
    const at = css.indexOf(selector);
    assert.notEqual(at, -1, `${selector} missing`);
    assert.match(css.slice(at, at + 500), /user-select:\s*none/);
  }
  const contentStart = css.search(/^\.chat-content \{/m);
  const content = css.slice(contentStart, contentStart + 200);
  assert.match(content, /user-select:\s*text/);
});

test("Whole Chat is the only attachment drop target and is invisible at rest", () => {
  assert.match(chat, /document\.body\.addEventListener\("dragenter"/);
  assert.match(chat, /document\.body\.classList\.add\("drop-target"\)/);
  assert.doesNotMatch(html, /chat-attachment-controls|<select[^>]+chat-exchange/);
  assert.match(html, />Browse…<\/button>/);
  assert.match(html, />From the attachments folder<\/button>/);
  assert.match(chat, /`From the \$\{attachmentFolder\.name\} folder`/);
  assert.match(chat, /`The \$\{attachmentFolder\.name\} folder is empty`/);
  assert.match(chat, /`Copying from the \$\{attachmentFolder\.name\} folder…`/);
  assert.match(css, /\.chat-page\.drop-target::after/);
});

test("A native attachment context refusal is visible in its bubble", () => {
  assert.match(chat, /if \(file\.outcome \|\| \/cannot read\/i\.test\(note\)\) return \{ state: "this model cannot read it", alarm: true/);
  assert.match(css, /\.attachment-bubble-state\.alarm\{color:var\(--alarm\)\}/);
});

test("one named bubble rule dresses messages and every attachment 2u1", () => {
  assert.match(tokens, /\.chat-bubble[^}]*\{[^}]*border:1px solid var\(--raised-edge\)[^}]*border-radius:var\(--raised-radius\)[^}]*box-shadow:var\(--raised-shadow\)/);
  assert.match(chat, /content\.className = "chat-content chat-bubble"/);
  assert.match(chat, /createAttachmentBubble\(document, file/);
  assert.match(deliverables, /root\.className = "attachment-bubble chat-bubble"/);
});

test("one raised definition dresses every raised surface 2u4", () => {
  assert.match(tokens, /--raised-edge:#000;--raised-radius:calc\(var\(--chat-size,12px\) \* 5 \/ 6\);--raised-radius-tight:calc\(var\(--chat-size,12px\) \/ 4\);--raised-fill:#15181C;--raised-fill-deep:#0D1014;--raised-shadow:inset 0 1px 0 rgba\(232,238,244,\.10\),0 3px 8px rgba\(0,0,0,\.38\)/);
  for (const selector of [".chat-bubble", ".chat-work-summary", ".chat-step-summary", ".thinking-line", ".file-chip", ".shell-menu", ".chat-attach-menu", ".setting-menu", ".approval-card", ".plan-proposal", ".settings-page", ".confirm-popover", ".settings-error-panel", ".chat-jump", ".chat-input-wrap", ".chat-live-status"])
    assert.match(tokens, new RegExp(selector.replaceAll(".", "\\.")));
  assert.match(tokens, /background:var\(--raised-fill\)!important;border:1px solid var\(--raised-edge\)!important;border-radius:var\(--raised-radius\)!important;box-shadow:var\(--raised-shadow\)!important/);
});

test("the message box folds its buttons and status remains the newest visible entry 2u2", () => {
  assert.match(html, /class="chat-input-wrap">\s*<div id="chat-attachments"[\s\S]*?<textarea id="chat-task"/);
  assert.match(css, /\.chat-input-wrap\s*\{[^}]*display:grid[^}]*height:112px/s);
  assert.match(css, /data-composer-layout="row"[^}]*\.chat-input-actions\s*\{[^}]*flex-direction:row/s);
  assert.match(css, /data-composer-layout="shut"[^}]*\.chat-input-actions\s*\{[^}]*display:none/s);
  assert.match(css, /\.chat-live-status\s*\{[^}]*position:sticky[^}]*bottom:calc\(var\(--ct\) \* 2 \/ 3\)[^}]*width:max-content/s);
  assert.match(chat, /if \(showStatus && \(statusLine\.parentElement !== log \|\| log\.lastElementChild !== statusLine\)\) log\.append\(statusLine\)/);
});

test("every chat chip derives its box and glyph measures from the chat text size 2u3", () => {
  assert.match(tokens, /--raised-radius:calc\(var\(--chat-size,12px\) \* 5 \/ 6\);--raised-radius-tight:calc\(var\(--chat-size,12px\) \/ 4\)/);
  for (const selector of [".chat-work-summary", ".chat-narration-line .chat-step-summary", ".file-chip", ".thinking-line", ".tool-tick", ".chat-live-status", ".attachment-bubble"])
    assert.match(css, new RegExp(selector.replaceAll(".", "\\.") + "\\s*\\{[^}]*calc\\(var\\(--ct\\)", "s"));
  assert.match(css, /\.attachment-bubble-preview,\.attachment-bubble-type\{[^}]*width:calc\(var\(--ct\) \* 19 \/ 6\)[^}]*height:calc\(var\(--ct\) \* 19 \/ 6\)/);
  assert.match(css, /\.attachment-bubble-remove\{[^}]*width:calc\(var\(--ct\) \* 2\)[^}]*height:calc\(var\(--ct\) \* 2\)/);
});

test("attachment remove drops one ready file and cancels only a readable PDF in progress 2u1", () => {
  assert.match(chat, /function removeQueuedAttachment\(file\)[\s\S]*queuedAttachments\.filter\(\(item\) => item !== file\)/);
  assert.match(chat, /remove: \(\) => removeQueuedAttachment\(file\)/);
  assert.match(chat, /remove: \(\) => void removeActiveAttachment\(upload\)/);
  assert.match(chat, /api\("\/api\/attachments\/stop", \{ upload_id: upload\.id \}\)/);
  assert.match(chat, /cancelledUploads\.add\(upload\.id\)/);
});

// The operator's three lower-chat controls. "the enter and stop
// buttons can be combined into one… i want a small microphone in place of the
// attachment icon… move the attachment icon directly above the microphone."
test("the paperclip replaces the duplicate token readout, and the composer holds the mic and one send/stop", () => {
  // Same control, same menu, same hover text; only its home changed.
  // Item 2ha: the three composer controls are one family of line art, so the
  // paperclip is an SVG in the shared glyph box rather than an emoji drawn by
  // whatever font the host has.
  assert.match(html, /class="chat-input-actions"[\s\S]{0,200}class="chat-attach-wrap"[\s\S]{0,400}id="chat-attach"[\s\S]{0,1200}id="chat-mic"/);
  assert.match(css, /\.chat-attach-wrap \{ position: relative; display:block; width:24px; height:24px; \}/);
  assert.doesNotMatch(html, /id="chat-readout"|id="chat-readout-meter"/);
  assert.match(html, /class="chat-composer-row"/);
  // A mic where the paperclip was, then ONE send/stop control.
  assert.match(html, /class="chat-input-actions"[\s\S]*id="chat-mic"[\s\S]*class="chat-submit-actions"[\s\S]*id="chat-send"/);
  assert.doesNotMatch(html, /id="chat-stop"/);
  assert.match(css, /\.chat-pending-attachments:empty\s*\{\s*display:\s*none/);
});

test("the composer stop state wins the send button's transparent background", () => {
  assert.match(css, /#chat-send\.stop-sign\.active[\s\S]*?background:var\(--alarm\)/);
  assert.doesNotMatch(chat, /busy\s*\?\s*["']model busy["']/);
});

test("Pending approval is pinned above the composer with zero idle space", () => {
	assert.match(html, /id="chat-pending-approval" class="pending-approval" hidden[\s\S]*class="chat-composer-row"/);
	assert.match(chat, /session\?\.pending_approval \|\| session\?\.pending_repo_policy \? "waiting for you"/);
	assert.match(chat, /setProperty\(pendingApproval, "hidden", !\(session\?\.pending_approval \|\| session\?\.pending_repo_policy \|\| worker\)\)/);
	assert.match(css, /\.pending-approval\[hidden\]\s*\{\s*display:\s*none/);
	assert.match(css, /\.chat-list-state\.waiting,\.chat-list-state\.offline \{ background: var\(--alarm\); \}/);
});

test("Composer sends during an active run and reports projected queue count", () => {
	assert.doesNotMatch(chat, /Run in progress|queue_depth/);
	// Item 2ge: the one control routes by the state it is IN. Typing stays
	// enabled while a run is live and a message sent then still queues behind
	// the stop — 2fg's hold rules are untouched.
	assert.match(chat, /if \(send\.dataset\.mode === "stop"\) return void stopRun\(\);/);
	assert.match(chat, /void submit\(\);/);
	assert.match(chat, /const queueText = queued \? `queued \(\$\{queued\}\)/);
});

test("Degraded accounting is labeled estimated in the Chat occupancy bar", () => {
  assert.match(chat, /value\.estimated \? "estimated · " : ""/);
});

test("the transcript-tail status owns queue operator pending and unreachable state", async () => {
  assert.match(html, /id="chat-log"[\s\S]*id="chat-status-line"[\s\S]*id="chat-notice"[\s\S]*id="chat-retry-model"/);
  assert.doesNotMatch(html + chat, /chat-run-as-you/);
  assert.match(chat, /const busyLine = activity \|\| \(busy \? "waiting for model" : ""\)/);
	for (const path of ["chat.js", "chat-activity.js"]) assert.doesNotMatch(await readFile(new URL(path, import.meta.url), "utf8"), /first token/i);
  assert.doesNotMatch(chat, /0 tokens processing/);
	assert.doesNotMatch(chat, /model busy · \$\{busy\.host/);
	assert.match(chat, /session\.connection_id \|\| session\.b_connection/);
  assert.match(chat, /queued \(\$\{queued\}\).*waiting for model/);
  assert.match(chat, /Run as you · until/);
  assert.match(chat, /filter\(\(entry\) => !\["operator\.context", "message\.queued", "run\.queued"\]/);
  assert.match(chat, /log\.lastElementChild !== statusLine/);
  assert.match(css, /\.chat-live-status \{[^}]*padding:calc\(var\(--ct\) \/ 3\) calc\(var\(--ct\) \* 2 \/ 3\)/);
  assert.match(css, /\.chat-resize-handle \{ height:4px; min-height:4px; cursor:row-resize/);
});

test("Operator mode lives only in Settings Security and states the defeated boundary", () => {
  assert.match(settings, /Run everything as me for 20 minutes/);
  assert.match(settings, /defeats the service-account OS boundary for every tool in every chat/);
  assert.match(settings, /data-action="operator-context"/);
  assert.match(settings, /"shell\.identity",\s*"operator\.context",\s*"shell\.credential"/);
  assert.match(settings, /const enabled = !\(store\.config\.shell\?\.operator_context \?\? store\.shell_identity\?\.operator_context\);[\s\S]*api\("\/api\/config", \{shell:\{operator_context:enabled\}\}\)/);
  assert.match(settings, /const connectionList = \(\) => Array\.isArray\(store\.connections\) \? store\.connections : \[\]/);
  assert.doesNotMatch(shell, /shell-operator-status/);
});

test("an unprovisioned refusal offers setup or the timed operator window", () => {
  assert.match(chat, /Set up now \(one Windows prompt\)/);
  assert.match(chat, /Run as you for 20 minutes/);
  assert.match(chat, /api\("\/api\/config", \{ shell: \{ operator_context: true \} \}\)/);
});

test("Chat navigation owns Plan Escape and the Settings save-or-discard guard", () => {
	assert.match(shell, /event\.key === "Escape" && page !== "chat"/);
	assert.match(settings, /Save unsaved settings before returning to Chat\?/);
	assert.match(settings, /openedFrom = "";\s*closeSettings\("chat"\)/);
	assert.match(shell, /after: \(\) => openSide\(`agent_\$\{session\.role === "d" \? "d" : "b"\}`, session\.id, "chat"\)/);
});

test("No-agent and empty Plan invitations are explicit and Console links to active tools", () => {
  assert.match(chat, /No agent connected — add one in/);
  assert.match(chat, /operator-off-48\.png/);
  // Item 2fc: the Plan page lists plans; with none it says how to add one.
  assert.match(plan, />No plans yet\. \+ adds one for a folder\.<\/p>/);
  // Item 2iq (b): the tools link became the summary of the fold that now holds
  // the toggles, because they have no other home and stay on Agents behind it.
  assert.match(consoleHTML, /id="panel-tools-count"[^>]*>0 tools active<\/span>/);
});

test("web_search and delegate remain file-configured without new Settings controls", () => {
	assert.match(app, /\["web_search", "delegate"\]\.includes\(tool\.name\)/);
	assert.match(app, /counts\.replaceChildren\(\.\.\.\(store\.tools \|\| \[\]\)\.map/);
});

test("new chats expose no folder selection surface", () => {
  assert.doesNotMatch(consoleHTML, /new-chat-workspace|New chat…/);
  assert.doesNotMatch(shell, /pick-folder|Folder path|Browse|Last plan|planRepoEditor|pending_bind/);
  assert.match(shell, /\{ connection_id: connectionID \}/);
  assert.doesNotMatch(shell, /source_session_id/);
});

test("Composer is five lines with no placeholder and expands upward", () => {
  assert.match(html, /textarea id="chat-task" rows="5" aria-label="Task"><\/textarea>/);
  // The composer, not the whole document: item 2no moved the Plan's own markup into
  // this file, and its search box and folder field carry placeholders of their own.
  const composer = html.slice(html.indexOf('<footer id="chat-composer"'), html.indexOf("</footer>", html.indexOf('<footer id="chat-composer"')));
  assert.doesNotMatch(composer, /placeholder=/);
  assert.match(css, /height:\s*112px/);
	// Item 2ld (a) and (b): the composer takes the height the operator dragged the
	// top edge to; the two predetermined heights and the control that stepped through
	// them are gone.
	assert.match(css, /--composer-height/);
	assert.doesNotMatch(css, /\.chat-composer\.expanded textarea/);
	assert.match(css, /grid-template-columns:\s*minmax\(0, 1fr\)/);
	assert.match(html, /class="chat-input-wrap">\s*<div id="chat-attachments"[\s\S]*?<textarea id="chat-task"/);
	assert.match(tokens, /\.chat-input-wrap[^}]*\{[^}]*background:var\(--raised-fill\)!important[^}]*border:1px solid var\(--raised-edge\)!important/);
	assert.match(css, /\.chat-composer textarea \{[\s\S]*?padding:\s*7px 42px 7px 9px;[\s\S]*?background:\s*transparent;[\s\S]*?border:\s*0;/);
	assert.match(css, /\.chat-input-actions \{[^}]*right:6px;[^}]*bottom:6px;[^}]*flex-direction:column/);
	assert.doesNotMatch(css, /#chat-expand/);
	assert.doesNotMatch(html, /id="chat-expand"/);
  assert.match(html, /id="chat-send"[^>]+aria-label="Send"[^>]*>[\s\S]{0,40}composer-glyph/);
  assert.match(css, /#chat-send \{[\s\S]*?height: 24px;[\s\S]*?min-height: 24px;/);
  assert.match(css, /\.chat-input-actions \.stop-sign \{ width:24px; height:24px;/);
  assert.doesNotMatch(html, />Send<\/button>/);
});

test("Chat rows use the full width and preserve spoken authors", () => {
	assert.match(css, /\.chat-entry\s*\{[\s\S]*grid-template-columns:\s*minmax\(0, 1fr\)/);
	assert.doesNotMatch(chat, /chat-speaker/);
	assert.match(chat, /setAttribute\(view\.row, "aria-label"/);
	assert.match(chat, /chat-notice-author chat-response-author/);
	assert.match(css, /\.chat-render-failure \.chat-content\s*\{[\s\S]*white-space:\s*nowrap/);
});

test("Model-unreachable transcript notices are flat and stay outside response counts", () => {
	assert.match(chat, /filter\(hasVisibleChatContent\)/);
	assert.match(chat, /entry\.event\?\.type === "run\.stopped" && entry\.event\?\.data\?\.reason === "model_unreachable"[\s\S]*grouped\.push\(entry\);[\s\S]*response = null/);
	assert.match(chat, /entry\.text \|\| session\.model_unreachable\?\.host/);
	assert.doesNotMatch(chat, /data\.reason === "model_unreachable"[\s\S]{0,500}createElement\("details"\)/);
	assert.match(chat, /retryModel\.onclick/);
});

test("Repository policy is a pinned full-content trust decision", () => {
  assert.match(chat, /Trust this repo's policy\?/);
  assert.match(chat, /policy\.content/);
  assert.match(chat, /policy\.diff/);
  assert.match(chat, /\["Yes, for this chat","policy-approve"\]/);
  assert.match(chat, /\["Just once","policy-once"\]/);
  assert.match(chat, /\["No","policy-deny"\]/);
  assert.match(chat, /Technical detail/);
});

test("chat has no workspace bind offer", () => {
	assert.doesNotMatch(chat, /pending_bind|workspace-bind-card|\/api\/bind/);
});

test("Reduced motion remains zero-duration", () => {
  assert.match(css, /prefers-reduced-motion:\s*reduce[\s\S]*animation-duration:\s*0ms\s*!important/);
});

test("A run stopped mid-tool is a flat transcript line, never folded into Steps (item 2fg)", () => {
	assert.match(chat, /entry\.event\?\.type === "run\.stopped" && entry\.event\?\.data\?\.reason === "aborted_mid_tool"\)[\s\S]*grouped\.push\(entry\);/);
	assert.match(chat, /data\.reason === "aborted_mid_tool"[\s\S]{0,200}stopped mid-tool/);
});

// Item 2gs (v1.2.1/W1): a journal id that appears twice gets two rows.
//
// The transcript rendered the operator's chats out of order — s6 showed
// m-5, m-9 and only then m-1, m-3. Nothing in the ordering code was wrong:
// his journal repeats message ids, the view cache was keyed by the id, so the
// second occurrence was handed the first one's DOM node, and insertBefore
// MOVES a node it already holds. Eighteen entries became twelve rows in the
// order they were last moved to.
test("a repeated entry key is drawn as its own row, not moved", async () => {
  const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
  // The per-pass key, and both caches using it rather than the raw key.
  assert.match(chat, /function viewKeyFor\(entry\) \{/);
  assert.match(chat, /if \(!usedEntryViews\.has\(entry\.key\)\) return entry\.key;/);
  assert.match(chat, /entryViews\.set\(viewKey, view\)/);
  assert.match(chat, /usedEntryViews\.add\(viewKey\)/);
  // Both renderers that cache a row take the per-pass key.
  const renderEntry = chat.slice(chat.indexOf("function renderEntry(session, entry)"));
  assert.match(renderEntry.slice(0, 900), /const viewKey = viewKeyFor\(entry\);/);
  const renderResponse = chat.slice(chat.indexOf("function renderResponse(session, entry)"));
  assert.match(renderResponse.slice(0, 300), /const viewKey = viewKeyFor\(entry\);/);
  // The raw key must no longer be what the cache is keyed by, or the collapse
  // comes back.
  assert.doesNotMatch(chat, /entryViews\.set\(entry\.key, view\)/);
  assert.doesNotMatch(chat, /usedEntryViews\.add\(entry\.key\)/);
  // A repeat is never dropped: that would hide part of the record.
  assert.doesNotMatch(chat, /entries\.filter\([^)]*seen[^)]*\)/);
});

// Item 2lj. Windows checks these files out with CRLF, so anything that slices
// or counts lines normalizes first.
const chatsPage = await readFile(new URL("./settings-chats.js", import.meta.url), "utf8").then((t) => t.replace(/\r\n/g, "\n"));
const configGo = await readFile(new URL("../../internal/config/config.go", import.meta.url), "utf8").then((t) => t.replace(/\r\n/g, "\n"));

// (a): two rows in Chats, and only two.
test("Chats offers only the three reading controls", () => {
  assert.match(chatsPage, /type="number" min="9" max="32" step="1" data-path="chat\.text_size"/);
  assert.match(chatsPage, /context\.menuSetting\("chat\.typeface", "typeface"/);
  assert.match(chatsPage, /context\.menuSetting\("chat\.text_color", "text colour"/);
  assert.equal([...chatsPage.matchAll(/data-path="chat\.[a-z_]+"/g)].length, 1);
  assert.equal([...chatsPage.matchAll(/context\.menuSetting\("chat\./g)].length, 2);
});

// (c): the operator asked for the dyslexia face to be listed like any other --
// "just include it as a font dont make it special or anything". Nothing in the
// page may mark any face as an accessibility option.
test("no typeface is marked as an accessibility option", () => {
  // What the reader sees, not what the source says about itself: this file's
  // own comments explain why the rule exists and must not trip it.
  const rendered = chatsPage.split("\n").filter((line) => !/^\s*(\/\/|\*|\/\*)/.test(line)).join("\n");
  for (const forbidden of [/dyslexi[ac][^"]/i, /accessib/i, /readability option/i, /recommended/i]) {
    const hit = rendered.match(forbidden);
    if (hit) assert.fail(`the Chats page explains a face: ${hit[0]}`);
  }
  assert.match(rendered, /"OpenDyslexic"/, "OpenDyslexic is not offered at all");
});

// (c): the list the page draws and the list the config accepts are the same
// list, so a face can never be offered and then refused on save.
test("typefaces come from the installed list and a missing saved face remains visible", () => {
  assert.match(chatsPage, /const SHIPPED_FACES = \["IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic"\]/);
  assert.match(chatsPage, /const installed = context\.fontFamilies\(\)/);
  assert.match(chatsPage, /if \(!installed\.includes\(chosen\)\) faces\.unshift\(\{ value: chosen, label: chosen, missing: true/);
  assert.doesNotMatch(configGo, /ChatTypefaces/);
});

// (b): one base size, and every size in the chat stylesheet a ratio of it. W0
// counted 38 hard-coded declarations here; a new one would not scale and would
// break the proportions the step is supposed to keep.
test("the chat stylesheet hard-codes no type size", () => {
  const body = css.replace(/\r\n/g, "\n").replace(/--ct:\s*var\(--chat-size,\s*12px\)/, "--ct: configured");
  const offenders = [...body.matchAll(/font(?:-size)?:\s*[^;]*?\b\d+px/g)].map((match) => match[0]);
  assert.deepEqual(offenders, [], `the chat surface still hard-codes a size: ${offenders.join("; ")}`);
  assert.match(css, /--ct: var\(--chat-size,\s*12px\)/);
});

// (b) and (d): the apply path is the two custom properties and nothing else.
test("the reading settings apply as they are set", () => {
  assert.match(chat, /function applyReadingSettings\(\)/);
  assert.match(chat, /root\.style\.setProperty\("--chat-size"/);
  assert.match(chat, /root\.style\.setProperty\("--chat-face"/);
  assert.match(chat, /root\.style\.setProperty\("--chat-ink"/);
  assert.match(chat, /if \(event\.type === "config\.changed"\) applyReadingSettings\(\);/);
});

// (c): both shipped faces are declared, because a font named but absent falls
// back silently and the reader who chose it would never know.
test("both shipped faces are declared against files that exist", async () => {
  for (const [family, file] of [
    ["Atkinson Hyperlegible", "atkinson-hyperlegible-latin-400-normal.woff2"],
    ["OpenDyslexic", "opendyslexic-latin-400-normal.woff2"],
  ]) {
    assert.ok(tokens.includes(`font-family:"${family}"`), `${family} has no @font-face`);
    assert.ok(tokens.includes(file), `${family} does not name its file`);
    const bytes = await readFile(new URL(`../assets/fonts/${file}`, import.meta.url));
    assert.ok(bytes.length > 1000, `${file} is not a font`);
  }
  const licence = await readFile(new URL("../assets/fonts/LICENSE-FONTS.md", import.meta.url), "utf8");
  for (const family of ["Atkinson Hyperlegible", "OpenDyslexic", "IBM Plex Sans", "IBM Plex Mono"]) {
    assert.ok(licence.includes(family), `${family} is shipped without a licence line`);
  }
});

test("Chats is the minimal ordered profile reading approval context delete surface 2sb", () => {
  const settingsMain = settings.slice(settings.indexOf("function group("));
  assert.match(settingsMain, /data-profile-select/);
  assert.match(settingsMain, /api\("\/api\/profiles", \{ action: "switch", name: event\.target\.value \}\)/);
  assert.match(settings, /function navSections\(\) \{ return sectionLabels; \}/);
  assert.doesNotMatch(chatsPage, /root path|show Plan|repeat limit|queue depth|max concurrent|reflection floor/i);
  const order = [
    chatsPage.indexOf('subhead("Reading")'),
    chatsPage.indexOf("${renderRunPage(context)}"),
    chatsPage.indexOf('subhead("Context")'),
    chatsPage.indexOf('subhead("Delete")'),
  ];
  assert.ok(order.every((at) => at >= 0) && order.every((at, index) => index === 0 || at > order[index - 1]), `Chats order ${order}`);
  assert.match(settings, /menuSetting\("approval\.mode", "approval mode"/);
  assert.match(settings, /<strong\$\{appearance\(option\)\}>\$\{html\(option\.label\)\}<\/strong>\$\{option\.detail/);
});

test("user messages are right-side composer-matched bubbles without changing other rows 2sl", () => {
  assert.match(css, /\.chat-user\s*\{[^}]*display:flex[^}]*justify-content:flex-end[^}]*padding-right:12px/s);
  assert.doesNotMatch(chat, /chat-speaker/);
  assert.match(css, /\.chat-user \.chat-content\s*\{[^}]*width:max-content[^}]*max-width:80%[^}]*border-bottom-right-radius:var\(--raised-radius-tight\)!important/s);
  assert.match(tokens, /\.chat-bubble[^}]*\{[^}]*background:var\(--raised-fill\)!important[^}]*border:1px solid var\(--raised-edge\)!important/);
  assert.match(chat, /view\.userText\.className = "chat-user-text"/);
  assert.match(css, /\.chat-user-text,\.chat-response-answer,\.chat-composer textarea\s*\{\s*color:var\(--chat-ink,var\(--ink\)\)/);
  assert.match(css, /\.chat-content code,\.chat-content pre\s*\{\s*color:var\(--ink\)/);
  assert.match(css, /\.chat-content a\s*\{\s*color:var\(--signal\)/);
  assert.match(css, /\.chat-user \.chat-content\s*\{\s*font-size:\s*calc\(var\(--ct\) \* 7 \/ 6\)/);
});

test("queued user turns are visible and Stop returns their words and files 2tl", () => {
	assert.match(chat, /queuedDrafts = new Map\(\)/);
	assert.match(chat, /session\.queued_message_ids/);
	assert.match(chat, /"queued · waiting for model" : "queued"/);
	assert.match(chat, /\[returned, input\.value\]\.filter\(Boolean\)\.join\("\\n"\)/);
	assert.match(chat, /queuedAttachments = \[\.\.\.attachments, \.\.\.queuedAttachments\.filter/);
	assert.match(chat, /api\("\/api\/stop", \{ session_id: session\.id \}\)/);
	assert.match(chat, /session\.run\?\.last_stop_reason !== "model_unreachable"/);
	assert.match(css, /\.chat-queued-state[^}]*color:var\(--mute\)/);
});

test("the 2qe palette has a signal green and accessible ink and mute contrast", () => {
  const colours = Object.fromEntries([...tokens.matchAll(/--(bezel|well|ink|mute|signal):(#(?:[0-9a-f]{6}))/gi)].map((match) => [match[1], match[2]]));
  // The requested #6E7A88 is only 4.36:1 on #0D1014. #707D8B is the nearest
  // two/three-channel lift that clears the simultaneously required 4.5:1.
  assert.deepEqual(colours, { bezel: "#0D1014", well: "#080A0D", ink: "#E8EEF4", mute: "#707D8B", signal: "#3CF08A" });
  const luminance = (hex) => {
    const channels = hex.slice(1).match(/../g).map((part) => parseInt(part, 16) / 255).map((value) => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4);
    return 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2];
  };
  const contrast = (left, right) => (Math.max(luminance(left), luminance(right)) + 0.05) / (Math.min(luminance(left), luminance(right)) + 0.05);
  for (const background of [colours.well, colours.bezel]) {
    assert.ok(contrast(colours.ink, background) >= 7);
    assert.ok(contrast(colours.mute, background) >= 4.5);
  }
  assert.match(tokens, /\.shell-session-lamp\[data-state=ready\]\{background:var\(--signal\)\}/);
  assert.match(css, /\.chat-list-state\.running \{ background: var\(--signal\); \}/);
  assert.match(css, /\.chat-list-row\.selected \{[^}]*box-shadow: inset 2px 0 0 var\(--signal\)/);
  assert.match(tokens, /focus-visible[^}]*outline:1px solid var\(--signal\)/);
});

test("the live status is transcript text and the composer edge is a four-pixel handle 2qm", () => {
  assert.match(css, /\.chat-live-status\s*\{[^}]*padding:calc\(var\(--ct\) \/ 3\) calc\(var\(--ct\) \* 2 \/ 3\)[^}]*font:500 calc\(var\(--ct\) \* 11 \/ 12\)\/var\(--ct\) var\(--mono\)/s);
  assert.match(css, /\.chat-composer\s*\{[^}]*gap:\s*0/s);
  assert.match(css, /\.chat-resize-handle\s*\{[^}]*height:4px[^}]*cursor:row-resize/s);
  assert.doesNotMatch(html + css, /chat-status-strip/);
});

test("the etched green current follows only a live run and reduced motion removes it 2qe", () => {
  assert.match(html, /class="etch-current" aria-hidden="true"/);
  assert.match(chat, /document\.body\.classList\.toggle\("run-active", isRunning\(session\)\)/);
  assert.match(tokens, /\.run-active \.etch-current\s*\{[^}]*opacity:\.0[1-6][^}]*animation:[^}]*6s/s);
  assert.match(tokens, /mask(?:-image)?:url\("\.\.\/assets\/etching\.png"\)/);
  assert.match(tokens, /@media\s*\(prefers-reduced-motion:\s*reduce\)[\s\S]*\.etch-current\s*\{display:none/);
});
