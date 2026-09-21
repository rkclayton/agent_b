import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const css = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const shell = await readFile(new URL("./shell.js", import.meta.url), "utf8");
const tokens = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
const settings = (await Promise.all([
  "settings.js", "settings-connections.js", "settings-general.js", "settings-context.js", "settings-run.js",
  "settings-delivery.js", "settings-about.js", "settings-workspace.js", "settings-security.js",
].map((name) => readFile(new URL(`./${name}`, import.meta.url), "utf8")))).join("\n");
const plan = await readFile(new URL("../plan.html", import.meta.url), "utf8");
const consoleHTML = await readFile(new URL("../index.html", import.meta.url), "utf8");

test("Chat has fence-only copy and documents composer keys", () => {
  assert.doesNotMatch(chat, /Copy message|messageCopy|assistantCopyText/);
  assert.match(html, /title="Send · Enter sends · Shift\+Enter newline"/);
});

test("Chat selection excludes chrome while preserving message content", () => {
  for (const selector of [".chat-speaker", ".thinking-line", ".tool-tick", ".chat-notice-row", ".chat-jump"]) {
    const at = css.indexOf(selector);
    assert.notEqual(at, -1, `${selector} missing`);
    assert.match(css.slice(at, at + 500), /user-select:\s*none/);
  }
  const content = css.slice(css.indexOf(".chat-content {"), css.indexOf(".chat-content {") + 200);
  assert.match(content, /user-select:\s*text/);
});

test("Whole Chat is the only attachment drop target and is invisible at rest", () => {
  assert.match(chat, /document\.body\.addEventListener\("dragenter"/);
  assert.match(chat, /document\.body\.classList\.add\("drop-target"\)/);
  assert.doesNotMatch(html, /chat-attachment-controls|<select[^>]+chat-exchange/);
  assert.match(html, />Browse…<\/button>/);
  assert.match(html, />From attachments<\/button>/);
  assert.match(css, /\.chat-page\.drop-target::after/);
});

test("A native attachment context refusal is visible beside its chip", () => {
  assert.match(chat, /if \(attachment\.outcome\)/);
  assert.match(chat, /outcome\.textContent = attachment\.outcome/);
  assert.match(css, /\.chat-attachment-warning \{ color: var\(--alarm\); \}/);
});

// The operator's three lower-chat controls. "the enter and stop
// buttons can be combined into one… i want a small microphone in place of the
// attachment icon… move the attachment icon to the bar above chat on the far
// right directly up from where it is now."
test("the paperclip replaces the duplicate token readout, and the composer holds the mic and one send/stop", () => {
  // Same control, same menu, same hover text; only its home changed.
  // Item 2ha: the three composer controls are one family of line art, so the
  // paperclip is an SVG in the shared glyph box rather than an emoji drawn by
  // whatever font the host has.
  assert.match(html, /id="chat-status-strip"[\s\S]{0,500}class="chat-attach-wrap"[\s\S]{0,400}id="chat-attach"[\s\S]{0,200}composer-glyph/);
  assert.match(css, /\.chat-status-strip \.chat-attach-wrap \{ margin-left:auto; \}/);
  assert.doesNotMatch(html, /id="chat-readout"|id="chat-readout-meter"/);
  assert.match(html, /class="chat-composer-row"/);
  // A mic where the paperclip was, then ONE send/stop control.
  assert.match(html, /class="chat-input-actions"[\s\S]*id="chat-mic"[\s\S]*class="chat-submit-actions"[\s\S]*id="chat-send"/);
  assert.doesNotMatch(html, /id="chat-stop"/);
  assert.match(css, /\.chat-pending-attachments:empty\s*\{\s*display:\s*none/);
});

test("Pending approval is pinned above the composer with zero idle space", () => {
	assert.match(html, /id="chat-pending-approval" class="pending-approval" hidden[\s\S]*class="chat-composer-row"/);
	assert.match(chat, /session\?\.pending_approval \|\| session\?\.pending_repo_policy \? "waiting for you"/);
	assert.match(chat, /pendingApproval\.hidden = !\(session\?\.pending_approval \|\| session\?\.pending_repo_policy \|\| worker\)/);
	assert.match(css, /\.pending-approval\[hidden\]\s*\{\s*display:\s*none/);
	assert.match(tokens, /\.agent-tab-robot\.waiting\{color:var\(--alarm\)/);
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

test("State strip owns queue operator pending and unreachable state without chat rows", () => {
  assert.match(html, /id="chat-status-strip"[\s\S]*id="chat-notice"[\s\S]*id="chat-retry-model"/);
  assert.doesNotMatch(html + chat, /chat-run-as-you/);
  // Item 2eo: a busy or unreachable model is the whole strip line, never both.
  assert.match(chat, /const modelLine = unreachable \? "model unreachable" : busy \? "model busy" : ""/);
	assert.doesNotMatch(chat, /model busy · \$\{busy\.host/);
	assert.match(chat, /session\.server_id \|\| session\.b_profile/);
  assert.match(chat, /queued \(\$\{queued\}\).*waiting for model/);
  assert.match(chat, /operator mode · until/);
  assert.match(chat, /filter\(\(entry\) => !\["operator\.context", "message\.queued", "run\.queued"\]/);
  assert.match(css, /\.chat-status-strip \{ min-height:24px/);
});

test("Operator mode lives only in Settings Security and states the defeated boundary", () => {
  assert.match(settings, /Run everything as me for 20 minutes/);
  assert.match(settings, /defeats the service-account OS boundary for every tool in every chat/);
  assert.match(settings, /data-action="operator-context"/);
  assert.match(settings, /api\("\/api\/config", \{shell:\{operator_context:!store\.shell_identity\?\.operator_context\}\}\)/);
  assert.match(settings, /const serverProfiles = \(\) => Array\.isArray\(store\.servers\) \? store\.servers : \[\]/);
  assert.doesNotMatch(shell, /shell-operator-status/);
});

test("No-agent and empty Plan invitations are explicit and Console links to active tools", () => {
  assert.match(chat, /No agent connected — add one in/);
  assert.match(chat, /operator-off-48\.png/);
  // Item 2fc: the Plan page lists plans; with none it says how to add one.
  assert.match(plan, />No plans yet\. \+ adds one for a folder\.<\/p>/);
  assert.match(consoleHTML, /id="panel-tools-link"[^>]*>0 tools active<\/a>/);
});

test("New chat uses the fixed left plus and history uses the agent right-click menu", () => {
  assert.match(html, /id="app-shell"[^>]+data-page="chat"/);
  assert.match(shell, /left\.append\(newChatButton, newChatMenu, tabs\)/);
  assert.match(shell, /hasD \? showRoleMenu/);
  assert.match(shell, /: void createChat\("agent_b"\)/);
  assert.doesNotMatch(shell, /wrap\.append\(newChatButton\)|wrap\.append\(add\)/);
  assert.match(shell, /oncontextmenu/);
  assert.match(shell, /agent-chat-rename/);
  // Item 2gq (v1.2.5): close deletes, so the separate permanent-delete control
  // is gone and the history row carries the name, a rename and the x.
  assert.doesNotMatch(shell, /agent-chat-delete/);
  assert.match(shell, /row\.append\(summary, rename, close\)/);
  assert.doesNotMatch(html + css, /chat-list|chat-list-toggle/);
  assert.doesNotMatch(html, /chat-clear-conversation|Clear conversation/);
  assert.match(shell, /source_session_id: source\.id/);
  assert.match(shell, /agent_d · \$\{name\} — plan/);
  assert.match(shell, /role: "d"/);
  assert.match(shell, /button\("", name, `agent-tab/);
  assert.match(shell, /Stop it before closing the chat/);
});

test("new chats expose no folder selection surface", () => {
  assert.doesNotMatch(consoleHTML, /new-chat-workspace|New chat…/);
  assert.doesNotMatch(shell, /pick-folder|Folder path|Browse|Last plan|planRepoEditor|pending_bind/);
  assert.match(shell, /\{ source_session_id: source\.id \}/);
  assert.match(shell, /source && source\.role !== "d"/);
});

test("Composer is five lines with no placeholder and expands upward", () => {
  assert.match(html, /textarea id="chat-task" rows="5" aria-label="Task"><\/textarea>/);
  assert.doesNotMatch(html, /placeholder=/);
  assert.match(css, /height:\s*112px/);
	assert.match(css, /\.chat-composer\.expanded textarea[\s\S]*height:\s*min\(50vh, 520px\)/);
	assert.match(css, /grid-template-columns:\s*minmax\(0, 1fr\)/);
	assert.match(css, /\.chat-pending-attachments,[\s\S]*\.chat-input-wrap\s*\{\s*grid-column:\s*1/);
	assert.match(css, /\.chat-input-wrap \{[^}]*border-radius:8px;[^}]*box-shadow:inset/);
	assert.match(css, /\.chat-composer textarea \{[\s\S]*?padding:\s*7px 64px 7px 9px;[\s\S]*?border:\s*0;[\s\S]*?border-radius:\s*8px;/);
	assert.match(css, /\.chat-input-actions \{[^}]*right:6px;[^}]*bottom:6px;[^}]*flex-direction:column/);
	assert.match(css, /#chat-expand\s*\{[\s\S]*position:\s*absolute;[\s\S]*top:\s*4px;[\s\S]*right:\s*4px/);
  assert.match(html, /id="chat-send"[^>]+aria-label="Send"[^>]*>[\s\S]{0,40}composer-glyph/);
  assert.match(css, /#chat-send \{[\s\S]*?height: 24px;[\s\S]*?min-height: 24px;/);
  assert.match(css, /\.chat-input-actions \.stop-sign \{ width:24px; height:24px;/);
  assert.doesNotMatch(html, />Send<\/button>/);
});

test("Chat uses the narrow monospace label gutter and the agent_b tab restores its robot", () => {
	assert.match(css, /\.chat-entry\s*\{[\s\S]*grid-template-columns:\s*72px minmax\(0, 1fr\)/);
	assert.match(chat, /speaker\(agentAuthor\(session\), true\)/);
	assert.match(chat, /function speaker\(name, agent = false\)[\s\S]*if \(agent\)[\s\S]*assets\/agent\.svg/);
	assert.match(css, /\.chat-render-failure \.chat-content\s*\{[\s\S]*white-space:\s*nowrap/);
	assert.match(shell, /agentID === "agent_b"[\s\S]*agent-tab-robot[\s\S]*assets\/agent\.svg/);
	assert.match(tokens, /\.agent-tab-robot\{[^}]*width:20px;height:20px/);
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
