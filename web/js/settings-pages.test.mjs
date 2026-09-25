import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { renderAboutPage } from "./settings-about.js";
import { renderChatsPage } from "./settings-chats.js";
import { renderConnectionsPage } from "./settings-connections.js";
import { renderContextPage } from "./settings-context.js";
import { renderGeneralPage } from "./settings-general.js";
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
    expanded: new Set(), armed: new Set(), drafts: new Map(), errors: new Map(), probeMessages: new Map(),
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
  assert.match(page, /server started <time class="settings-server-started">unknown<\/time>, v0\.49\.0/);
  assert.match(page, /checked <time class="settings-update-checked">never<\/time>/);
});

test("Security renders the LAN switch and detected confirmation list", () => {
	const context = pageContext();
	context.store.config.shell.allow_local_network = false;
	context.hardeningStatus.detected_local_subnets = ["192.168.50.0/24"];
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

test("Connections summary row never renders decoder detail verbatim", () => {
	const context = pageContext();
	const connection = { id: "fake", label: "Fake", base_url: "http://fake/", capabilities: { findings: ["probe failed: Connection returned a web page, not model API JSON. Add the API path to base_url.", "probe detail: invalid character '<' looking for beginning of value"] } };
	context.connectionList = () => [connection];
	context.probeMessages.set("fake", { message: "Test failed — Connection returned a web page, not model API JSON. Add the API path to base_url.", alarm: true });
	const page = renderConnectionsPage(context);
	assert.match(page, /Test failed — Connection returned a web page, not model API JSON/);
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
