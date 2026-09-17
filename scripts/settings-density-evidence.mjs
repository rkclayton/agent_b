// Render every Settings page from a given checkout of the modules and report
// the control inventory and the visible prose. Run once against HEAD~ (before)
// and once against the working tree (after); the inventory must be identical.
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const root = process.cwd();
const which = process.argv[2] || "after";
const ref = process.argv[3] || "";

let moduleDir = path.join(root, "web", "js");
let scratch = "";
if (ref) {
  scratch = fs.mkdtempSync(path.join(os.tmpdir(), "settings-before-"));
  fs.mkdirSync(path.join(scratch, "web", "js"), { recursive: true });
  fs.mkdirSync(path.join(scratch, "web", "css"), { recursive: true });
  const files = execFileSync("git", ["ls-tree", "-r", "--name-only", ref, "web/"], { cwd: root, encoding: "utf8" })
    .split("\n").filter((name) => name.endsWith(".js") || name.endsWith(".mjs") || name.endsWith(".css"));
  for (const name of files) {
    const body = execFileSync("git", ["show", `${ref}:${name}`], { cwd: root, encoding: "utf8" });
    const target = path.join(scratch, name);
    fs.mkdirSync(path.dirname(target), { recursive: true });
    fs.writeFileSync(target, body, "utf8");
  }
  moduleDir = path.join(scratch, "web", "js");
}

const blank = () => "";
const html = String;
const attr = String;
function row(label, control, extra = "", hint = "") {
  return `<div class="setting-row ${extra}"${hint ? ` title="${attr(hint)}"` : ""}><label>${html(label)}</label><div>${control}</div></div>`;
}
function subhead(label, hint = "") {
  return `<div class="settings-subhead"${hint ? ` title="${attr(hint)}"` : ""}>${html(label)}</div>`;
}

function context() {
  return {
    active: "", store: {
      active: "", sessions: {}, servers: [], build: { tag: "v0.61.0", commit: "abcdef0" },
      config: { workspace: "C:\\workspace", context: { soft_pct: 0.75, summary_pct: 0.85, accounting: "auto" }, chat: {}, run: {}, approval: {}, deliver: {}, memory: {}, tools: { read_file: {} }, shell: { service_account: {} }, signing: {}, sandbox: {} },
      shell_credential: {}, shell_identity: {}, sandbox: {}, serving_facts: {},
    },
    expanded: new Set(), armed: new Set(), drafts: new Map(), errors: new Map(), probeMessages: new Map(),
    workspaceState: [], operatorFileState: { attachment_files: 0, attachment_bytes: 0, instruction_found: [] },
    shellCredentialMessage: "", shellCredentialAlarm: false,
    serviceAccountStatus: { loaded: false, supported: true, exists: false, administrator: false },
    serviceAccountBusy: false, serviceAccountMessage: "", serviceAccountAlarm: false,
    hardeningStatus: { loaded: false, supported: true, applied: false, detected_local_subnets: ["192.168.50.0/24", "10.0.0.0/24", "172.16.0.0/24"] },
    hardeningBusy: false, hardeningMessage: "", hardeningAlarm: false,
    signingStatus: { loaded: false, supported: true, configured: false, can_manage: true, files: [], certificates: [] },
    signingBusy: false, signingMessage: "", signingAlarm: false,
    notificationStatus: {}, notificationBusy: false, notificationMessage: "", notificationAlarm: false,
    serverProfiles: () => [], row, subhead, field: blank, text: blank, number: blank, numberControl: blank,
    textarea: blank, secret: blank, toggle: blank, choices: blank, approvalChoices: blank, copyRow: blank,
    currentValue: (_p, fallback) => fallback, issue: blank, profileReason: blank,
    html, attr, selectedHardeningServerID: blank,
    operatorStatusView: () => ({ active: false, label: "off", src: "", srcset: "" }),
  };
}

const { renderSecurityPage } = await import(pathToFileURL(path.join(moduleDir, "settings-security.js")).href);
const pages = { Security: () => renderSecurityPage("shell", null, context()) };

const report = {};
for (const [name, render] of Object.entries(pages)) {
  const markup = render();
  const prose = [...markup.matchAll(/<p class="settings-note">([\s\S]*?)<\/p>/g)].map((m) => m[1].replace(/<[^>]+>/g, "").trim());
  const hints = [...markup.matchAll(/title="([^"]{12,})"/g)].map((m) => m[1]);
  const controls = [
    ...[...markup.matchAll(/data-action="([a-z-]+)"/g)].map((m) => `action:${m[1]}`),
    ...[...markup.matchAll(/data-path="([^"]+)"/g)].map((m) => `path:${m[1]}`),
    ...[...markup.matchAll(/<input[^>]*id="([^"]+)"/g)].map((m) => `input:${m[1]}`),
    ...[...markup.matchAll(/<select[^>]*id="([^"]+)"/g)].map((m) => `select:${m[1]}`),
    ...[...markup.matchAll(/data-local-subnet\b/g)].map(() => "input:local-subnet"),
  ].sort();
  report[name] = { visible_prose_lines: prose.length, prose, hover_hints: hints.length, controls, control_count: controls.length };
}

console.log(JSON.stringify({ which, ref: ref || "working tree", report }, null, 1));
if (scratch) removeTreeWithinAllowedRoots(scratch, [os.tmpdir()], "settings-density scratch cleanup");
