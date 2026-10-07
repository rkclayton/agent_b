import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { renderAboutPage } from "./settings-about.js";
import { renderChatsPage } from "./settings-chats.js";
import { connectionFailureSentence, renderConnectionsPage } from "./settings-connections.js";
import { renderContextPage } from "./settings-context.js";
import { renderGeneralPage } from "./settings-general.js";
import { renderProfilesPage } from "./settings-profiles.js";
import { renderRunPage } from "./settings-run.js";
import { renderSecurityPage } from "./settings-security.js";
import { renderWorkspacePage } from "./settings-workspace.js";

const blank = () => "";
const row = (label, control, extra = "") => `<div class="setting-row ${extra}"><label>${label}</label><div>${control}</div></div>`;
const inputRow = (_path, label) => row(label, "<input>");

function pageContext() {
  return {
    store: {
      active: "", sessions: {}, connections: [], build: { tag: "v0.49.0", commit: "abcdef0" },
      config: { workspace: "C:\\workspace", context: { soft_pct: 0.75, summary_pct: 0.85, accounting: "auto" }, chat: {}, run: {}, approval: {}, deliver: {}, memory: {}, tools: {}, shell: { service_account: {} }, signing: {}, sandbox: {} },
      shell_credential: {}, shell_identity: {}, sandbox: {}, serving_facts: {}, signature: { files: [{ status: "Valid", timestamped: true }] },
    },
    expanded: new Set(), armed: new Set(), drafts: new Map(), errors: new Map(), probeMessages: new Map(), typedModels: new Set(), advancedConnections: new Set(),
    workspaceState: [], operatorFileState: { attachment_files: 0, attachment_bytes: 0, instruction_found: [] },
    shellCredentialMessage: "", shellCredentialAlarm: false,
    serviceAccountStatus: { loaded: false, supported: true, exists: false, administrator: false },
    serviceAccountBusy: false, serviceAccountMessage: "", serviceAccountAlarm: false,
    hardeningStatus: { loaded: false, supported: true, applied: false },
    hardeningBusy: false, hardeningMessage: "", hardeningAlarm: false,
    signingStatus: { loaded: false, supported: true, configured: false, can_manage: false, files: [] },
    signingBusy: false, signingMessage: "", signingAlarm: false,
    connectionList: () => [], row, subhead: (label, hint = "") => `<div class="settings-subhead">${label}</div>${hint ? `<p class="settings-subhead-note">${hint}</p>` : ""}`, field: blank, text: inputRow, number: inputRow, numberControl: () => "<input>",
    textarea: inputRow, secret: inputRow, toggle: inputRow, choices: inputRow, selectSetting: inputRow, approvalChoices: () => row("approval", "<button>boundary-only</button>"), copyRow: row,
    currentValue: (_path, fallback) => fallback, issue: blank, connectionReason: blank,
    html: String, attr: String, errorMarkup: (message, _key, className = "field-error") => `<p class="${className}">${message}</p>`, selectedHardeningConnectionID: blank,
    operatorStatusView: () => ({ active: false, label: "off", src: "", srcset: "" }),
  };
}

test("every Settings page renderer accepts the controller context", () => {
  const context = pageContext();
  const pages = [
    renderConnectionsPage(context), renderContextPage(null, context), renderRunPage(context), renderChatsPage(null, context),
    renderAboutPage(context), renderWorkspacePage(context),
    renderSecurityPage("shell", null, context), renderGeneralPage("sessions", null, context),
    renderGeneralPage("tools", null, context), renderGeneralPage("memory", null, context),
  ];
  assert.equal(pages.length, 10);
  for (const page of pages) assert.equal(typeof page, "string");
});

test("button-owned text survives a Settings redraw and Skills explains arrival", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  const profiles = fs.readFileSync(new URL("settings-profiles.js", import.meta.url), "utf8");
  assert.match(controller, /actionDrafts\.set\(event\.target\.id, event\.target\.value\)/);
  assert.match(controller, /for \(const \[id, value\] of actionDrafts\)/);
  assert.match(profiles, /Skills are on when added; use the switch to turn one off/);
});

test("Attachment ingest shows the editable disk limit and its reason", () => {
  const context = pageContext();
  context.store.config.tools = { attachments: { max_bytes: 268435456 } };
  context.number = (path, label, value, _min, _allowEmpty, _suffix, _disabled, _kind, hint) => `${path}|${label}|${value}|${hint}`;
  assert.match(renderGeneralPage("tools", null, context), /tools\.attachments\.max_bytes\|max upload bytes\|268435456\|disk; not a prompt limit/);
});

test("Profiles lists agent-layer memory with a named remove action", () => {
	const context = pageContext();
	context.store.active = "s1";
	context.store.sessions.s1 = { agent_id: "agent-b", agent_memory_content: "Notes about how this agent works with the operator:\n- ping output\n- durable preference\n" };
	const page = renderProfilesPage(context);
	assert.match(page, /Agent memory/);
	assert.match(page, /ping output/);
	assert.match(page, /data-action="remove-agent-memory" data-id="ping output"/);
});

test("Memory marks notes about the agent without adding a control", () => {
	const source = fs.readFileSync(new URL("settings-connections.js", import.meta.url), "utf8");
	assert.match(source, /about_agent:[\s\S]*about the agent/);
	assert.doesNotMatch(source, /data-action=["'`]keep-agent-memory/);
	const context = pageContext();
	context.store.active = "s1";
	context.store.sessions.s1 = { agent_id: "agent-b", agent_memory_content: "Notes:\n- STAY IN WORKSPACE [scope: user, about-agent: yes]" };
	const page = renderProfilesPage(context);
	assert.match(page, /STAY IN WORKSPACE \[scope: user, about the agent\]/);
	assert.match(page, /data-action="remove-agent-memory"/);
	assert.doesNotMatch(page, /data-action=["'`]keep-agent-memory/);
});

test("Profiles renders the Hermes preview with one import action and include rows", () => {
	const context = pageContext();
	context.hermesPreview = { rows: [
		{ id: "persona:SOUL.md", name: "SOUL.md", kind: "persona", detail: "not imported — it becomes an agent of your own when agents ship", included: false, selectable: false },
		{ id: "memory:MEMORY.md", name: "MEMORY.md", kind: "memory", detail: "18 bytes", included: true, selectable: true },
		{ id: "secret:FIRST_KEY", name: "FIRST_KEY", kind: "secret", detail: "imported in the next step", included: false, selectable: false },
	] };
	const page = renderProfilesPage(context);
	assert.match(page, /Import from Hermes/);
	assert.match(page, /SOUL\.md[\s\S]*not imported/);
	assert.match(page, /MEMORY\.md[\s\S]*data-action="hermes-toggle"/);
	assert.match(page, /FIRST_KEY[\s\S]*imported in the next step/);
});

test("all rendered Settings rows have one direct label and one control cell", () => {
  const context = pageContext();
  const pages = [
    renderConnectionsPage(context), renderContextPage(null, context), renderRunPage(context), renderChatsPage(null, context),
    renderAboutPage(context), renderWorkspacePage(context), renderSecurityPage("shell", null, context),
    renderGeneralPage("sessions", null, context), renderGeneralPage("tools", null, context), renderGeneralPage("memory", null, context),
  ];
  for (const page of pages) {
    const rows = [...page.matchAll(/<div class="setting-row[^\"]*"[^>]*>/g)].length;
    const grammatical = [...page.matchAll(/<div class="setting-row[^\"]*"[^>]*>\s*<label(?:\s[^>]*)?>[\s\S]*?<\/label>\s*<div>/g)].length;
    assert.equal(grammatical, rows, page);
  }
});

test("Settings navigation has the required eight sections in order", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  const labels = [...controller.matchAll(/\["(?:agents|activity|connections|profiles|chats|notifications|shell|about)", "([^"]+)"\]/g)].map((match) => match[1]);
  assert.deepEqual(labels, ["Agents", "Activity", "Connections", "Profiles", "Chats", "Notifications", "Security", "About"]);
});

test("Delivery has no Settings page while file configuration remains supported", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  assert.doesNotMatch(controller, /settings-delivery|\["delivery", "Delivery"\]|renderDeliveryPage/);
  assert.deepEqual(pageContext().store.config.deliver, {});
});

test("About exposes only build identity and exact clock values to screenshot masks", () => {
  const context = pageContext();
  context.row = (_label, value) => value;
  const page = renderAboutPage(context);
  assert.match(page, /<code class="settings-build-text">v0\.49\.0 · abcdef0 · signed<\/code>/);
	assert.match(page, /class="settings-server-value">server started <time class="settings-server-started">unknown<\/time>, v0\.49\.0/);
  assert.match(page, /checked <time class="settings-update-checked">never<\/time>/);
});

test("Security renders the LAN switch and detected confirmation list", () => {
	const context = pageContext();
	context.store.config.shell.allow_local_network = false;
	context.hardeningStatus.detected_local_subnets = ["192.168.1.0/24"];
	context.row = (label, value, _extra, hint) => `${label}:${value}${hint ? ` title=${hint}` : ""}`;
	const page = renderSecurityPage("shell", null, context);
	assert.match(page, /Allow my local network/);
	assert.match(page, /192\.168\.1\.0\/24/);
	assert.match(page, /link-local, cloud metadata and Agent_b's own listener remain refused/i);
	// The subnets ride the switch's own row rather than a block beneath it.
});

test("Security names each service identity state and offers only Set up or Repair", () => {
	const cases = [
		[{ state: "missing", exists: false, action: "Set up" }, /account not created[\s\S]*>Set up</],
		[{ state: "missing_credential", exists: true, action: "Repair" }, /account exists · credential missing[\s\S]*>Repair</],
		[{ state: "invalid_credential", exists: true, action: "Repair" }, /account exists · credential rejected[\s\S]*>Repair</],
		[{ state: "ready", exists: true, action: "" }, /account and credential ready/],
	];
	for (const [status, expected] of cases) {
		const context = pageContext();
		context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, ...status };
		context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
		context.selectedHardeningConnectionID = () => "local";
		assert.match(renderSecurityPage("shell", null, context), expected);
	}
});

test("Security says when Windows policy blocks service identity setup", () => {
	const context = pageContext();
	context.serviceAccountStatus = {
		loaded: true, supported: true, exists: false, state: "missing",
		execution_policy_message: "Windows policy on this machine disables PowerShell scripts (AllSigned, set by MachinePolicy); the service identity cannot be set up here",
	};
	assert.match(renderSecurityPage("shell", null, context), /Windows policy on this machine disables PowerShell scripts \(AllSigned, set by MachinePolicy\); the service identity cannot be set up here/);
});

test("Security restores the service identity toggle and names a locked Repair dead end", () => {
	const context = pageContext();
	context.toggle = (path, label, value) => `${path}:${label}:${value}`;
	context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "locked_out", action: "Repair" };
	context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
	context.selectedHardeningConnectionID = () => "local";
	context.standingGrants = [{ id: "folder:C:\\work", kind: "folder", subject: "C:\\work" }];
	const page = renderSecurityPage("shell", null, context);
	// Item 2np (a): the toggle is no longer a Save draft — it is the setup, and it acts
	// at once. Saving it was refused by the server with a pointer to "its Security setup
	// flow", which is the message the operator met when he simply turned it on.
	assert.match(page, /data-action="service-identity-toggle"/);
	assert.doesNotMatch(page, /shell\.service_account\.enabled:Service identity/);
	assert.match(page, /account locked out · wait, then Repair/);
	assert.match(page, /Repair unavailable — account locked out/);
	assert.match(page, /data-action="setup-service-account" disabled/);
	assert.match(page, /data-action="revoke-standing-grant"/);
});

test("Security draws exactly one phone section: broker pairing titled Phone 2pl", () => {
	const context = pageContext();
	context.brokerStatus = { state: "holding", paired_device: "Phone" };
	const security = renderSecurityPage("shell", null, context);
	assert.equal([...security.matchAll(/settings-subhead[^>]*>Phone</g)].length, 1);
	assert.doesNotMatch(security, /Phone access|away from home|phone-enrol|phone-revoke|phone-push-toggle/i);
	for (const action of ["broker-pair", "broker-revoke"]) assert.match(security, new RegExp(`data-action="${action}"`));
});

test("Connections summary row never renders decoder detail verbatim", () => {
	const context = pageContext();
	const connection = { id: "fake", label: "Fake", base_url: "http://fake/", capabilities: { findings: ["probe failed: Connection returned a web page, not model API JSON. Add the API path to base_url.", "probe detail: invalid character '<' looking for beginning of value"] } };
	context.connectionList = () => [connection];
	context.probeMessages.set("fake", { message: "Test failed — Connection returned a web page, not model API JSON. Add the API path to base_url.", alarm: true });
	const page = renderConnectionsPage(context);
	// Item 2nb (g): the collapsed row carries the STATE WORD, not the sentence. The
	// sentence lives once, in the editor, under the field it is about — a whole message
	// here overflowed the row, and the operator saw three copies of one of them.
	// Item 2px (e): the word is the server's health state, "checking" until it has one.
	assert.match(page, /class="connection-state">checking/);
	assert.doesNotMatch(page, /Connection returned a web page/);
	// And the decoder's own words never reach the page at all, which is what this test
	// was written for.
	assert.doesNotMatch(page, /invalid character/);
});

test("Connections Test consumes only its one-line result and the picker lists independently", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  const connections = fs.readFileSync(new URL("settings-connections.js", import.meta.url), "utf8");
  assert.match(controller, /const discovered = await api\(`\/api\/connections\/\$\{encodeURIComponent\(id\)\}\/probe`, \{/);
  assert.match(controller, /base_url: current\(`/);
  assert.doesNotMatch(controller, /Saving before Test/);
  assert.match(controller, /alarm: discovered\.status !== "passed"/);
	assert.doesNotMatch(controller, /\n\s+applyProposedValues\(id, discovered\);/);
	assert.match(controller, /\/models`, \{ base_url/);
  assert.match(connections, /discovery\?\.models/);
  assert.match(connections, /<select class="setting-input"/);
  assert.match(connections, /discovery-note/);
	assert.match(connections, /split\(\/\[\\\\\/\]\//);
});

test("connection failures use the operator's eight exact sentences", () => {
  const cases = [
    ["Get http://box:8080/models: context deadline exceeded", "http://box:8080", "No answer from box:8080. It may be off, asleep or out of reach of this PC."],
    ["dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it.", "http://127.0.0.1:9", "Nothing is listening at 127.0.0.1:9."],
    ["dial tcp: lookup absent.test: no such host", "http://absent.test:8080", "The name absent.test was not found."],
    ["the endpoint wants an API key", "http://box:8080", "The server at box:8080 wants an API key."],
    ["the server refused the API key", "http://box:8080", "The server at box:8080 refused the API key."],
    ["Connection returned a web page, not model API JSON.", "http://box:8080", "Something answered at box:8080, but it is not a model server."],
    ["the endpoint answered but lists no models", "http://box:8080", "The server at box:8080 answered but lists no models."],
    ["a new failure nobody classified", "http://box:8080", "The test failed."],
  ];
  for (const [full, address, sentence] of cases) assert.equal(connectionFailureSentence(full, address), sentence);
});

test("a failed Test is one sentence and one details link, never a field error", () => {
  const context = pageContext();
  const full = "dial tcp 127.0.0.1:9: connectex: No connection could be made because the target machine actively refused it. ".repeat(6).trim();
  const connection = { id: "local", label: "Local", base_url: "http://127.0.0.1:9", model: "m", sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: { findings: [] } };
  context.connectionList = () => [connection];
  context.expanded.add("local");
  context.probeMessages.set("local", { message: "Nothing is listening at 127.0.0.1:9.", detail: full, alarm: true });
  const page = renderConnectionsPage(context);
  assert.equal((page.match(/Nothing is listening at 127\.0\.0\.1:9\./g) || []).length, 1);
  assert.equal((page.match(/>details<\/button>/g) || []).length, 1);
  assert.doesNotMatch(page, new RegExp(full.slice(0, 80).replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.doesNotMatch(page, /field-error/);
});

test("field errors are exact-path only and long Settings errors use the in-window panel", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  assert.match(controller, /function issue\(path\) \{\s*return errors\.get\(path\) \|\| "";/);
  assert.match(controller, /class="settings-error-panel"[^>]*role="dialog"[^>]*aria-modal="false"/);
  assert.match(controller, /data-action="error-details"/);
  assert.match(controller, /if \(event\.key === "Escape" && open && errorPanel\)/);
  assert.doesNotMatch(controller, /window\.open/);
});

test("600-character errors stay one sentence on every Settings page that paints errors 2po", () => {
  const long = `First sentence. ${"diagnostic words ".repeat(45)}`;
  const context = pageContext();
  context.errorMarkup = (message, key, className = "field-error") => `<p class="${className}">${message.split(". ")[0]}. <button data-action="error-details" data-detail-key="${key}">details</button></p>`;
  context.errors.set("connections.held", long);
  context.connectionList = () => [{ id: "held", label: "Held", base_url: "http://held/", sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: {} }];
  context.errors.set("tools.read_file", long);
  context.errors.set("workspace", long);
  context.errors.set("session.s1", long);
  context.issue = (path) => context.errors.get(path) || "";
  context.store.sessions.s1 = { id: "s1", label: "one", workspace: "C:\\fixture", connection_id: "held", run: { status: "idle" } };
  for (const [name, page] of [
    ["Connections", renderConnectionsPage(context)], ["Sessions", renderGeneralPage("sessions", null, context)],
    ["Tools", renderGeneralPage("tools", null, context)], ["Security/Folders", renderWorkspacePage(context)],
  ]) {
    assert.match(page, /First sentence\. <button[^>]+>details<\/button>/, name);
    assert.doesNotMatch(page, /diagnostic words diagnostic words/, name);
  }
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  for (const group of ["profiles", "shell", "shell.trusted_folders", "connections", "config"]) assert.match(controller, new RegExp(`(?:\\[|\\\")${group.replaceAll(".", "\\.")}`), group);
  assert.match(controller, /chat_root/); // Chats uses the exact field helper.
});

// Item 2nb (f): THE KEY IS NEVER TEXT. The field used to be rendered with the server's
// mask as its VALUE, so the placeholder for a stored key was a submittable string in an
// input that could be shown, copied or half-edited. It is empty now, with a note beside
// it, and what "show" reveals is only what was typed this session — because that is the
// only thing the field ever holds. Asserted on the source, because this suite renders
// pages with a stubbed field helper.
test("the API key field never carries the stored-key mask as its value", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	const secret = controller.slice(controller.indexOf("function secret("), controller.indexOf("function toggle("));
	assert.doesNotMatch(secret, /value="\$\{attr\(current\(path/, "the field still renders the stored value");
	assert.match(secret, /typedThisSession/, "the field does not render only what was typed");
	assert.match(secret, /control-note">\$\{typedThisSession \? "replacing the stored key" : "stored"\}/);
	assert.doesNotMatch(secret, /leave empty|paste the API key/);
});

test("stored connection keys reveal briefly, duplicate beside Test, and rows use a pencil 2qn", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	const connections = fs.readFileSync(new URL("settings-connections.js", import.meta.url), "utf8");
	assert.match(controller, /\/api\/connections\/\$\{encodeURIComponent\(id\)\}\/key/);
	assert.match(controller, /setTimeout\([^]*30000/);
	assert.doesNotMatch(controller.slice(controller.indexOf("async function duplicateConnection"), controller.indexOf("function uniqueID")), /copy\.credential\s*=\s*""/);
	assert.match(connections, /data-action="save-connection"[^]*data-action="duplicate-connection"/);
	assert.match(connections, /connectionIcons\.edit/);
	assert.doesNotMatch(connections, /aria-expanded="\$\{isOpen\}">Edit<\/button>/);
	assert.doesNotMatch(connections, /This server runs one model/);
});

// And the server refuses a masked value outright rather than silently dropping it.
test("the configuration route refuses an API key that still holds the mask", () => {
	const source = fs.readFileSync(new URL("../../internal/web/server_connections.go", import.meta.url), "utf8");
	assert.match(source, /func patchCarriesMaskedKey/);
	assert.match(source, /still holds the placeholder for a stored key/);
	// A refusal, not a delete: the old merge dropped an exact match and stored anything else.
	assert.match(source, /strings\.Contains\(value, maskedKeySentinel\)/);
});

// Item 2nb (b) and (i): while the walk runs, the sheet says which address is being
// tried and what it found, in item 2m4's ONE waiting element — determinate, addresses
// tried of the candidate total. It sat idle and unexplained for the length of the walk
// before this, which is why the operator thought Test had done nothing.
test("a running endpoint walk leaves a seat for the one waiting element, not a second indicator", () => {
	const context = pageContext();
	context.connectionList = () => [{ id: "walk", label: "Walk", base_url: "http://walk:8080/", _probing: true, sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: {} }];
	context.expanded.add("walk");
	context.probeMessages.set("walk", { walking: { line: "http://walk:8080 — wants an API key", processed: 2, total: 16 } });
	const page = renderConnectionsPage(context);
	// Item 2nn (c) moved the seat: the waiting element belongs INSIDE the control
	// that was pressed, not under the field, because a bar the operator has to go
	// looking for is a bar he reports as missing.
	assert.match(page, /data-probe-wait="walk"/, "no seat was left for the waiting element");
	assert.doesNotMatch(page, /data-connection-wait="walk"/, "the old seat under the field is still rendered too");
	// The page must NOT build its own indicator: item 2m4 says there is one element.
	assert.doesNotMatch(page, /class="wait"/, "the page built its own waiting element");
	assert.doesNotMatch(page, /discovery-note/, "the note and the wait are both shown");
});

test("the controller mounts the one waiting element into that seat, determinate", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	assert.match(controller, /import \{ setWaitProgress, waitElement \} from "\.\/wait\.js"/);
	// (2nn (c)) the seat is in the control now, and the walk's reports move the bar
	// in place rather than re-rendering the sheet out from under the operator.
	assert.match(controller, /data-probe-wait/);
	assert.match(controller, /setWaitProgress\(bar, moved\)/);
	assert.match(controller, /waitElement\(document, \{ line: walking\.line, processed: walking\.processed, total: walking\.total \}\)/);
	// (b): the walk's own report is what fills it, and it is cleared when Test answers.
	assert.match(controller, /event\.type === "connection\.discovering"/);
	assert.match(controller, /walking: null/);
});

// (d): picking a model fills the rest, and never overwrites a label the operator chose.
test("picking a model proposes the label only when the connection was never named", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	const filler = controller.slice(controller.indexOf("function fillFromPickedModel"), controller.indexOf("function applyProposedValues"));
	assert.match(filler, /!drafts\.has\(prefix \+ "label"\)/, "a label edited this session is not protected");
	assert.match(filler, /!connection\.label \|\| connection\.label === connection\.id/, "a label the operator chose is not protected");
	assert.match(filler, /applyProposedValues\(id, probeMessages\.get\(id\)\)/, "the values Test learned are not re-proposed");
	// The display name, not the path: a file-based model must not put a path in the label.
	assert.ok(filler.includes("split(/[\\\\/]/"), "the display name is not split on either separator");
});

// Item 2nc (a) and (d): THE OPERATOR'S CASE. A collapsed row, a connection held by a
// chat, Remove pressed — and the screen did not change. The server had always sent the
// row as the field and the handler had always kept it, but only the fields inside the
// EXPANDED body rendered it, so the reason was never on screen.
test("a refused removal is shown on the row itself, collapsed or not", () => {
	const context = pageContext();
	const connection = { id: "held", label: "Held", base_url: "http://held/", sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: {} };
	context.connectionList = () => [connection];
	// Collapsed: the row is not in `expanded`.
	context.errors.set("connections.held", "in use by the chat Siri (s12) — close that chat first, then remove this connection");
	const page = renderConnectionsPage(context);
	assert.match(page, /class="connection-refusal alarm"/, "the refusal is not on the row");
	assert.match(page, /close that chat first/, "the row does not say what to do");
	assert.match(page, /connection-row [^"]*refused/, "the row is not marked as refused");
});

test("a row with nothing refused carries no refusal line", () => {
	const context = pageContext();
	context.connectionList = () => [{ id: "fine", label: "Fine", base_url: "http://fine/", sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: {} }];
	const page = renderConnectionsPage(context);
	assert.doesNotMatch(page, /connection-refusal/);
});

// (a) again, from the controller: the row is expanded when the refusal lands, and a
// later success clears it so a refusal cannot outlive what it described.
test("a refused removal expands its row, and a successful one clears the refusal", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	const remover = controller.slice(controller.indexOf("async function removeConnection"), controller.indexOf("async function newSession"));
	assert.match(remover, /expanded\.add\(id\)/, "the row is not expanded when the refusal lands");
	assert.match(remover, /errors\.delete\(`connections\.\$\{id\}`\)/, "a successful removal leaves the old refusal on screen");
	assert.match(remover, /errors\.set\(error\.field \|\| `connections\.\$\{id\}`/, "the server's own field is not used");
});

// (c): the popover is anchored in the settings scroller's space, not the viewport's, so
// it stays with its control however far the sheet is scrolled.
test("the confirmation popover is anchored in the scroller's space and clamped", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	const anchor = controller.slice(controller.indexOf("const POPOVER_WIDTH"), controller.indexOf("function cancelConfirmation"));
	assert.match(anchor, /scroller\.scrollTop/, "the anchor ignores the sheet's scroll");
	assert.match(anchor, /getBoundingClientRect\(\)/);
	assert.match(anchor, /Math\.max\(0, top\)/, "the anchor is not clamped");
	const css = fs.readFileSync(new URL("../css/app.css", import.meta.url), "utf8");
	const popover = css.slice(css.indexOf(".confirm-popover {"), css.indexOf(".confirm-popover p"));
	assert.match(popover, /position: absolute/, "the popover is still fixed to the viewport");
	assert.doesNotMatch(popover, /position: fixed/);
	// And it is rendered inside the scroller, which is what makes absolute mean that.
	assert.match(controller, /settings-content" tabindex="-1">\$\{group\(label, content\[activeSection\]\(\), activeSection\)\}\$\{confirmPopover\(\)\}/);
});

// Item 2np's acceptance, recorded. The operator, with a screenshot of Settings →
// Security: "i tried to enable service identity and it told me to use the 'flow' i
// want this fixed. the toggle needs to work and not allude to some other section. the
// red text all over and i figure it meant repair clicked repair and that failed. tihs
// is a terrible UX help me fix the service identity transition UX".
//
// On his screen at once: a banner, the row label in red, the same sentence again under
// the toggle, "account not created" beside an account that existed, and a red box
// telling him to use a control that does not exist.

// (c) and (d): identity off with the account there is not an error, and the words say
// what is true.
test("identity off with the account present is not an error", () => {
  const context = pageContext();
  context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "disabled" };
  context.store.config.shell.service_account = { enabled: false, account: "agentb-svc" };
  context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
  context.selectedHardeningConnectionID = () => "local";
  const page = renderSecurityPage("shell", null, context);
  assert.match(page, /account exists · identity off/, "the account row still says the account is not created");
  assert.doesNotMatch(page, /account not created/);
  // (d): nothing red, and the setup panel is closed.
  assert.doesNotMatch(page, /lamp alarm/);
  assert.doesNotMatch(page, /<details class="settings-advanced" open>/);
});

// (c): every state the server can return has its own words, and "account not created"
// belongs to exactly one of them.
test("every service identity state has its own words", () => {
  const states = ["unsupported", "administrator", "locked_out", "missing", "missing_credential", "invalid_credential", "disabled", "credential_check_failed", "ready"];
  const seen = new Map();
  for (const state of states) {
    const context = pageContext();
    context.serviceAccountStatus = { loaded: true, supported: state !== "unsupported", administrator: state === "administrator", exists: state !== "missing", state };
    context.store.config.shell.service_account = { enabled: state === "ready", account: "agentb-svc" };
    context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
    context.selectedHardeningConnectionID = () => "local";
    const page = renderSecurityPage("shell", null, context);
    const line = /agentb-svc[^<]*/.exec(page)?.[0] ?? (state === "unsupported" ? "available only on Windows" : "");
    assert.ok(line.trim(), `${state} renders no account line`);
    assert.doesNotMatch(line, /undefined/, `${state} renders a placeholder`);
    seen.set(state, line.trim());
  }
  const notCreated = [...seen].filter(([, line]) => /account not created/.test(line)).map(([state]) => state);
  assert.deepEqual(notCreated, ["missing"], `"account not created" is shown for ${notCreated.join(", ")}`);
  // No two states share a line: a word that covers two states cannot be acted on.
  assert.equal(new Set(seen.values()).size, seen.size, `two states share their words: ${JSON.stringify([...seen])}`);
});

// (a) and (b): the toggle is the setup, and a failure is ONE sentence under it.
test("the toggle is the setup and its failure is one sentence in one place", () => {
  const context = pageContext();
  context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "disabled" };
  context.store.config.shell.service_account = { enabled: false, account: "agentb-svc" };
  context.serviceAccountMessage = "the account was provisioned but its protections were not applied: network policy NOT applied";
  context.serviceAccountAlarm = true;
  context.serviceAccountLog = "C:\\path\\service-identity.log";
  context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
  context.selectedHardeningConnectionID = () => "local";
  const page = renderSecurityPage("shell", null, context);
  // It acts at once; it is not a Save draft with a path.
  assert.match(page, /data-action="service-identity-toggle"/);
  assert.doesNotMatch(page, /data-path="shell\.service_account\.enabled"/);
  // Exactly one copy of the sentence, and the log path beside it as secondary text.
  // Visible copies, not the tooltip the same element carries in its title attribute.
  const copies = [...page.matchAll(/>[^<]*protections were not applied/g)].length;
  assert.equal(copies, 1, `the failure is shown ${copies} times`);
  assert.match(page, /service-identity\.log/);
  // (e): nothing names a control this page does not render.
  for (const missing of [/Reset password/, /Security setup flow/]) {
    assert.doesNotMatch(page, missing, "the section names a control or a flow that is not here");
  }
});

test("service identity shows truthful per-step outcomes and an openable log link", () => {
  const context = pageContext();
  context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "disabled" };
  context.store.config.shell.service_account = { enabled: false, account: "agentb-svc" };
  context.serviceAccountMessage = "service identity not set up";
  context.serviceAccountAlarm = true;
  context.serviceAccountLog = "C:\\Temp\\service-identity.log";
  context.serviceAccountSteps = [
    "account — PASS",
    "protections — PASS",
    "network — FAILED exit 1: DRIFT RemoteAddress",
  ];
  context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
  context.selectedHardeningConnectionID = () => "local";
  const page = renderSecurityPage("shell", null, context);
  for (const line of context.serviceAccountSteps) assert.match(page, new RegExp(line.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  assert.match(page, /<a[^>]+href="file:\/\/\/C:\/Temp\/service-identity\.log"[^>]*>open log<\/a>/i);
  assert.doesNotMatch(page, /network[^<]*applied[^<]*exit 1/i);
});

test("a broker refusal shows its code and detail with the pairing log link", () => {
  const context = pageContext();
  context.brokerStatus = { state: "not paired", last_error: "refused by the broker — malformed: pairing_id", log_path: "C:\\Data\\logs\\pairing.log" };
  const page = renderSecurityPage("shell", null, context);
  assert.match(page, /refused by the broker — malformed: pairing_id/);
  assert.match(page, /<a[^>]+href="file:\/\/\/C:\/Data\/logs\/pairing\.log"[^>]*>open pairing log<\/a>/i);
});

// (e), as a rule rather than one example: every failure sentence the setup path can
// produce is checked against the controls the section actually renders.
test("no service identity message names a control the section does not render", () => {
  const context = pageContext();
  context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "disabled" };
  context.store.config.shell.service_account = { enabled: false, account: "agentb-svc" };
  context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
  context.selectedHardeningConnectionID = () => "local";
  const rendered = renderSecurityPage("shell", null, context);
  const controls = new Set([...rendered.matchAll(/data-action="([a-z-]+)"/g)].map((match) => match[1]));
  assert.ok(controls.has("service-identity-toggle") && controls.has("setup-service-account"), [...controls].join(", "));
  // The words the server can send, read from the source rather than imagined.
  const source = fs.readFileSync(new URL("../../internal/web/service_account.go", import.meta.url), "utf8");
  const named = [...source.matchAll(/use ([A-Z][A-Za-z ]+?) (?:before|to)/g)].map((match) => match[1].trim());
  assert.deepEqual(named, [], `a failure text names ${named.join(", ")}, which is not a control on this page`);
});

// Item 2px CHECKS 2, 3 and 5: Edit shows every field before any Test or chosen
// model; the server's three models fill the picker; the lamp and word are the
// health state, the same in the header and the form.
test("the connection form is whole before a Test and its models come from the server", () => {
  const context = pageContext();
  const connection = { id: "acme", label: "acme", base_url: "http://acme:8080/", model: "", api_key: "", context: { n_ctx: 32768, reserve_output: 8192 }, reasoning: { enabled: false, control: "auto", preserve: false }, sampling: { thinking: {}, nonthinking: {} }, capabilities: {} };
  context.connectionList = () => [connection];
  context.expanded.add("acme");
  context.store.connection_health = { acme: { lamp: "amber", word: "no model chosen" } };
  const typed = renderConnectionsPage(context);
  for (const label of ["name", "address", "key", "model", "context size", "thinking", "reads images", "Eval", "Recommended", "Defaults", "credential ref", "reserve"]) {
    assert.ok(typed.includes(label), `${label} is not on the form before a Test`);
  }
  assert.doesNotMatch(typed, /Test to list models/);
  assert.equal([...typed.matchAll(/class="lamp amber"/g)].length, 2, "header and form head disagree");
  context.probeMessages.set("acme", { models: ["alpha", "beta", "gamma"] });
  const listed = renderConnectionsPage(context);
  for (const model of ["alpha", "beta", "gamma"]) assert.match(listed, new RegExp(`<option value="${model}"`));
});

test("2qx connection editor has exactly seven ordered fields, one closed Defaults group, and no explanatory prose", () => {
  const context = pageContext();
  const connection = { id: "acme", label: "acme", base_url: "http://acme:8080/", model: "alpha", api_key: "", reads_images: false, context: { n_ctx: 0, reserve_output: 8192 }, reasoning: { enabled: true, effort: "medium", valid_efforts: ["low", "medium", "high"], control: "auto", preserve: false }, sampling: { thinking: {}, nonthinking: {} }, capabilities: {} };
  context.connectionList = () => [connection];
  context.expanded.add("acme");
  context.connectionReason = () => "context unknown — enter the size";
  const rendered = renderConnectionsPage(context);
  const editor = rendered.slice(rendered.indexOf('<section class="connection-editor"'));
  const defaults = editor.match(/<details class="connection-defaults"[^>]*><summary>([^<]+)<\/summary>/);
  assert.equal(defaults?.[1], "Defaults");
  assert.doesNotMatch(defaults?.[0] || "", /\sopen(?:\s|>)/);
  const visible = editor.slice(0, editor.indexOf('<details class="connection-defaults"'));
  const labels = [...visible.matchAll(/<label[^>]*>([^<]*)<\/label>/g)].map((match) => match[1]).filter(Boolean);
  assert.deepEqual(labels, ["name", "address", "key", "model", "context size", "thinking", "reads images"]);
  assert.match(visible, /context unknown — enter the size/);
  for (const sentence of ["Model endpoints and their current probe state.", "This server runs one model", "The name this connection", "How much the model thinks"]) {
    assert.doesNotMatch(editor, new RegExp(sentence.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
  }
  const textNodes = [...editor.matchAll(/>([^<>]+)</g)].map((match) => match[1].trim()).filter(Boolean);
  const allowed = new Set([
    "acme", "http://acme:8080/", "name", "address", "key", "show", "model", "alpha", "type a name…",
    "context size", "context unknown — enter the size", "thinking", "low", "medium", "high", "reads images",
    "Test", "Eval", "Recommended", "Defaults", "Connection", "credential ref", "extract_url", "attachment handling",
    "auto", "native", "extract", "timeout", "probe mode", "full", "minimal", "off", "Reasoning &amp; context",
    "control", "chat_template_kwargs", "top_level", "server_flag", "none", "preserve", "reasoning cap", "reserve",
    "Sampling", "Thinking", "Non-thinking", "temperature", "top_p", "top_k", "min_p", "presence penalty",
    "repeat penalty", "llama.cpp only", "System prompt", "system prompt override", "Capabilities", "not probed",
    "no findings", "Save", "Duplicate",
  ]);
  assert.deepEqual(textNodes.filter((node) => !allowed.has(node)), [], `non-label/value/state/error text: ${textNodes.join(" | ")}`);
});
