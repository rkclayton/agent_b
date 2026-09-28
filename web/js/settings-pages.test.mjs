import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { renderAboutPage } from "./settings-about.js";
import { renderChatsPage } from "./settings-chats.js";
import { renderConnectionsPage } from "./settings-connections.js";
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
    textarea: inputRow, secret: inputRow, toggle: inputRow, choices: inputRow, approvalChoices: () => row("approval", "<button>boundary-only</button>"), copyRow: row,
    currentValue: (_path, fallback) => fallback, issue: blank, connectionReason: blank,
    html: String, attr: String, selectedHardeningConnectionID: blank,
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

test("Profiles lists agent-layer memory with a named remove action", () => {
	const context = pageContext();
	context.store.active = "s1";
	context.store.sessions.s1 = { agent_id: "agent-b", agent_memory_content: "Notes about how this agent works with the operator:\n- ping output\n- durable preference\n" };
	const page = renderProfilesPage(context);
	assert.match(page, /Agent memory/);
	assert.match(page, /ping output/);
	assert.match(page, /data-action="remove-agent-memory" data-id="ping output"/);
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
	context.hardeningStatus.detected_local_subnets = ["192.168.1.10/24"];
	// The explanatory sentence is hover text now rather than a visible line, so
	// the stub renders the hint the real row renders as a title attribute.
	context.row = (label, value, _extra, hint) => `${label}:${value}${hint ? ` title=${hint}` : ""}`;
	const page = renderSecurityPage("shell", null, context);
	assert.match(page, /Allow my local network/);
	assert.match(page, /192\.168\.50\.0\/24/);
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

test("Security restores the service identity toggle and names a locked Repair dead end", () => {
	const context = pageContext();
	context.toggle = (path, label, value) => `${path}:${label}:${value}`;
	context.serviceAccountStatus = { loaded: true, supported: true, administrator: false, exists: true, state: "locked_out", action: "Repair" };
	context.connectionList = () => [{ id: "local", label: "Local", base_url: "http://127.0.0.1:8080" }];
	context.selectedHardeningConnectionID = () => "local";
	context.standingGrants = [{ id: "folder:C:\\work", kind: "folder", subject: "C:\\work" }];
	const page = renderSecurityPage("shell", null, context);
	assert.match(page, /shell\.service_account\.enabled:Service identity:false/);
	assert.match(page, /account locked out · wait, then Repair/);
	assert.match(page, /Repair unavailable — account locked out/);
	assert.match(page, /data-action="setup-service-account" disabled/);
	assert.match(page, /data-action="revoke-standing-grant"/);
});

test("Security has one Phone access entry and the PWA stays transcript composer push only", () => {
	const context = pageContext();
	context.phoneAccess = { devices: [{ id: "one", name: "Phone", last_seen: "now" }], push_enabled: false };
	const security = renderSecurityPage("shell", null, context);
	assert.equal([...security.matchAll(/Phone access/g)].length, 1);
	for (const action of ["phone-enrol", "phone-revoke", "phone-push-toggle"]) assert.match(security, new RegExp(`data-action="${action}"`));
	const html = fs.readFileSync(new URL("../phone.html", import.meta.url), "utf8");
	assert.match(html, /id="phone-transcript"/);
	assert.match(html, /id="phone-composer"/);
	assert.match(html, /id="phone-push"/);
	assert.doesNotMatch(html, /settings|tools|plan|attachment/i);
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
	assert.match(page, /class="connection-state">failed/);
	assert.doesNotMatch(page, /Connection returned a web page/);
	// And the decoder's own words never reach the page at all, which is what this test
	// was written for.
	assert.doesNotMatch(page, /invalid character/);
});

test("Connections Test consumes endpoint discovery and renders its model picker", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  const connections = fs.readFileSync(new URL("settings-connections.js", import.meta.url), "utf8");
  assert.match(controller, /const discovered = await api\(`\/api\/connections\/\$\{encodeURIComponent\(id\)\}\/probe`, \{/);
  assert.match(controller, /base_url: current\(`/);
  assert.doesNotMatch(controller, /Saving before Test/);
  assert.match(controller, /discovered\.status === "model_required"/);
	assert.match(controller, /discovered\.changes\?\.base_url/);
  assert.match(connections, /discovery\?\.models/);
  assert.match(connections, /<select class="setting-input"/);
  assert.match(connections, /discovery-note/);
	assert.match(connections, /split\(\/\[\\\\\/\]\//);
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
	assert.match(secret, /leave empty to keep the stored key/);
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
	context.connectionList = () => [{ id: "walk", label: "Walk", base_url: "http://walk:8080/", sampling: { thinking: {}, nonthinking: {} }, context: {}, reasoning: {}, capabilities: {} }];
	context.expanded.add("walk");
	context.probeMessages.set("walk", { walking: { line: "http://walk:8080 — wants an API key", processed: 2, total: 16 } });
	const page = renderConnectionsPage(context);
	assert.match(page, /data-connection-wait="walk"/, "no seat was left for the waiting element");
	// The page must NOT build its own indicator: item 2m4 says there is one element.
	assert.doesNotMatch(page, /class="wait"/, "the page built its own waiting element");
	assert.doesNotMatch(page, /discovery-note/, "the note and the wait are both shown");
});

test("the controller mounts the one waiting element into that seat, determinate", () => {
	const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
	assert.match(controller, /import \{ waitElement \} from "\.\/wait\.js"/);
	assert.match(controller, /data-connection-wait/);
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
