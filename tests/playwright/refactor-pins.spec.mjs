import { readdir, readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

const settings = await readFile(new URL("../../web/js/settings.js", import.meta.url), "utf8");
const settingsDirectory = new URL("../../web/js/", import.meta.url);
const settingsViews = (await Promise.all(
  (await readdir(settingsDirectory))
    .filter((name) => name.startsWith("settings-") && name.endsWith(".js"))
    .map((name) => readFile(new URL(name, settingsDirectory), "utf8")),
)).join("\n");
const shell = await readFile(new URL("../../web/js/shell.js", import.meta.url), "utf8");

const settingsActions = [
  "add-connection", "adopt-instructions", "apply-hardening", "broker-cancel",
  "broker-confirm", "broker-pair", "broker-revoke", "check-update",
  "clear-notification", "clear-shell-credential", "close", "close-error-details",
  "close-session", "config-choice", "config-toggle", "connection-toggle", "copy",
  "create-profile", "credential-add", "credential-device-code", "credential-entra-add",
  "credential-remove", "credential-sign-in", "credential-sign-out", "delete-all-chats",
  "disable-service-account", "duplicate-connection", "empty-operator-attachments",
  "error-details", "export-diagnostics", "hermes-confirm", "hermes-preview",
  "hermes-toggle", "install-update", "local-network-toggle", "measure-connection",
  "new-session", "open-setup", "operator-context", "probe",
  "profile-memory-note-toggle", "profile-memory-toggle", "recommended-connection",
  "refresh-hardening", "refresh-service-account", "remove-agent-memory",
  "remove-connection", "remove-hardening", "rename-profile", "reset-session",
  "revoke-standing-grant", "revoke-workspace-policy", "save-notification",
  "save-setting", "save-settings", "service-identity-toggle", "session-tool-toggle",
  "setting-menu-toggle", "settings-section", "setup-service-account", "show-key",
  "sign-in-start", "skill-add-confirm", "skill-add-pick", "skill-import",
  "skill-remove", "skill-rescan", "skill-row-toggle", "skill-toggle",
  "store-shell-credential", "surface-visible", "switch-profile", "test-notification",
  "test-shell-credential", "trusted-folder-add", "trusted-folder-delete",
  "verify-hardening",
];

test("Settings keeps every button action wired before its handlers move", () => {
  for (const action of settingsActions) {
    expect(settings, `${action} has a dispatch branch`).toContain(`action === "${action}"`);
  }
  expect(settings + settingsViews).toContain("data-action=");
});

test("the shell keeps the controls whose wiring is split into named parts", () => {
  for (const control of [
    "chat-list-menu-button", "chat-list-new-folder", "chat-list-new",
    "chat-list-handle", "chat-list-resize", "shell-session-title",
  ]) expect(shell, control).toContain(control);
  for (const event of ["click", "keydown", "pointerdown", "pointermove", "pointerup"])
    expect(shell, event).toContain(event);
});
