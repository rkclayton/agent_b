import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

import { renderAboutPage } from "./settings-about.js";
import { renderConnectionsPage } from "./settings-connections.js";
import { renderContextPage } from "./settings-context.js";
import { renderDeliveryPage } from "./settings-delivery.js";
import { renderGeneralPage } from "./settings-general.js";
import { renderRunPage } from "./settings-run.js";
import { renderSecurityPage } from "./settings-security.js";
import { renderWorkspacePage } from "./settings-workspace.js";

const blank = () => "";

function pageContext() {
  return {
    store: {
      active: "", sessions: {}, servers: [], build: { tag: "v0.49.0", commit: "abcdef0" },
      config: { workspace: "C:\\workspace", context: { soft_pct: 0.75, summary_pct: 0.85, accounting: "auto" }, chat: {}, run: {}, approval: {}, deliver: {}, memory: {}, tools: {}, shell: { service_account: {} }, signing: {}, sandbox: {} },
      shell_credential: {}, shell_identity: {}, sandbox: {}, serving_facts: {},
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
    serverProfiles: () => [], row: blank, subhead: blank, field: blank, text: blank, number: blank, numberControl: blank,
    textarea: blank, secret: blank, toggle: blank, choices: blank, approvalChoices: blank, copyRow: blank,
    currentValue: (_path, fallback) => fallback, issue: blank, profileReason: blank,
    html: String, attr: String, selectedHardeningServerID: blank,
    operatorStatusView: () => ({ active: false, label: "off", src: "", srcset: "" }),
  };
}

test("every Settings page renderer accepts the controller context", () => {
  const context = pageContext();
  const pages = [
    renderConnectionsPage(context), renderContextPage(null, context), renderRunPage(context),
    renderDeliveryPage(context), renderAboutPage(context), renderWorkspacePage(context),
    renderSecurityPage("shell", null, context), renderGeneralPage("sessions", null, context),
    renderGeneralPage("tools", null, context), renderGeneralPage("memory", null, context),
  ];
  assert.equal(pages.length, 10);
  for (const page of pages) assert.equal(typeof page, "string");
});

test("About exposes only the changing build text to the screenshot mask", () => {
  const context = pageContext();
  context.row = (_label, value) => value;
  const page = renderAboutPage(context);
  assert.match(page, /<code class="settings-build-text">v0\.49\.0 · abcdef0<\/code>/);
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

test("Connections summary row never renders decoder detail verbatim", () => {
	const context = pageContext();
	const profile = { id: "fake", label: "Fake", base_url: "http://fake/", capabilities: { findings: ["probe failed: Connection returned a web page, not model API JSON. Add the API path to base_url.", "probe detail: invalid character '<' looking for beginning of value"] } };
	context.serverProfiles = () => [profile];
	context.probeMessages.set("fake", { message: "Test failed — Connection returned a web page, not model API JSON. Add the API path to base_url.", alarm: true });
	const page = renderConnectionsPage(context);
	assert.match(page, /Test failed — Connection returned a web page, not model API JSON/);
	assert.doesNotMatch(page, /invalid character/);
});

test("Connections Test consumes endpoint discovery and renders its model picker", () => {
  const controller = fs.readFileSync(new URL("settings.js", import.meta.url), "utf8");
  const connections = fs.readFileSync(new URL("settings-connections.js", import.meta.url), "utf8");
  assert.match(controller, /const discovered = await api\(`\/api\/servers\/\$\{encodeURIComponent\(id\)\}\/probe`\)/);
  assert.match(controller, /discovered\.status === "model_required"/);
  assert.match(connections, /discovery\?\.models/);
  assert.match(connections, /<select class="setting-input"/);
  assert.match(connections, /discovery-note/);
});
