import assert from "node:assert/strict";
import test from "node:test";

import { renderProfilesPage } from "./settings-profiles.js";
import { renderSecurityPage } from "./settings-security.js";

const row = (label, control) => `<div class="setting-row"><label>${label}</label><div>${control}</div></div>`;
const subhead = (label) => `<div class="settings-subhead">${label}</div>`;
const context = () => ({
  store: {
    active: "s",
    sessions: { s: { agent_memory_content: "- 2026-10-12 preference text [scope: user]" } },
    config: {
      profiles: { active: "one", names: ["one", "two"] },
      shell: { trusted_folders: [{ path: "C:\\work", added_at: "2026-10-12T00:00:00Z", source: "settings" }] },
      approval: { unattended: true }, sandbox: {},
    },
    shell_credential: {}, shell_identity: {}, sandbox: {},
  },
  expanded: new Set(), armed: new Set(), drafts: new Map(), errors: new Map(), actionDrafts: new Map(), standingGrants: [{ id: "host:x", kind: "host", subject: "x" }],
  row, subhead, html: String, attr: String, toggle: () => "", text: () => "", copyRow: row,
  connectionList: () => [], connectionReason: () => "", selectedHardeningConnectionID: () => "", operatorStatusView: () => ({ active: false, label: "off", src: "", srcset: "" }),
  serviceAccountStatus: { loaded: false }, serviceAccountSteps: [], hardeningStatus: { loaded: false, acl: {}, firewall: {} }, credentialList: [],
});

test("Profiles keeps notes behind a closed four-column memory table and removes user management 2tz", () => {
  const page = renderProfilesPage(context());
  assert.match(page, /data-action="profile-memory-toggle"/);
  assert.match(page, /Date[\s\S]*Kind[\s\S]*Note[\s\S]*Remove/);
  assert.match(page, /2026-10-12[\s\S]*user[\s\S]*preference text/);
  assert.doesNotMatch(page, /Create user|switch-profile|rename-profile/);
});

test("Security removes unattended and standing grants, tables Allowed Directories, and ends in Experimental 2tz", () => {
  const page = renderSecurityPage("shell", null, context());
  assert.doesNotMatch(page, /Unattended|Standing grants|revoke-standing-grant/);
  assert.match(page, /Allowed Directories[\s\S]*<th>Directory<\/th>[\s\S]*<th>Added<\/th>/);
  const heads = [...page.matchAll(/settings-subhead[^>]*>([^<]+)</g)].map((match) => match[1]);
  assert.deepEqual(heads.slice(0, 5), ["Run as you", "Allowed Directories", "Phone", "Credentials", "Experimental"]);
  assert.equal(heads.at(-1), "Experimental");
  assert.ok(page.indexOf("Service identity") < page.indexOf("Docker Sandbox"));
});
