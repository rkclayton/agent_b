import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { start } from "../../scripts/ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "../../scripts/removal-guard.mjs";

const run = promisify(execFile);
const repo = resolve(fileURLToPath(new URL("../..", import.meta.url)));
let root;
let harness;

test.beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "agentb-discovery-playwright-"));
  const exe = join(root, "Agent_b.exe");
  await run(join(repo, ".tools", "go", "bin", "go.exe"), ["build", "-o", exe, "./cmd/harness"], { cwd: repo, windowsHide: true });
  harness = await start({
    exe,
    appRoot: repo,
    data: join(root, "data"),
    modelIDs: ["alpha-model", "beta-model"],
    profileModel: "model",
  });
});

test.afterAll(async () => {
  await harness?.stop();
  if (root) removeTreeWithinAllowedRoots(root, [tmpdir()], "connection-discovery Playwright cleanup");
});

test("Setup and Connections share endpoint discovery and the model picker", async () => {
  const setup = await harness.context.newPage();
  await setup.goto(`${harness.base}/setup`);
  await setup.getByRole("button", { name: "Test" }).click();
  await expect(setup.locator(".discovery-note")).toHaveText(`found http://127.0.0.1:${harness.modelPort}`);
  await expect(setup.locator('[data-field="model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(setup.locator('[data-field="model"] option')).toHaveText(["model · not served", "alpha-model", "beta-model"]);
  await expect(setup.locator(".setup-feedback")).toContainText('Model "model" is not served');

  const settings = await harness.context.newPage();
  await settings.goto(`${harness.base}/chat?from=setup#settings/servers`);
  await settings.locator('.profile-summary[data-id="ui"]').click();
  await settings.locator('.profile-row:has(.profile-summary[data-id="ui"]) [data-action="probe"]').click();
  await expect(settings.locator(".profile-editor .discovery-note")).toHaveText(`found http://127.0.0.1:${harness.modelPort}`);
  await expect(settings.locator('[data-path="servers.ui.model"]')).toHaveJSProperty("tagName", "SELECT");
  await expect(settings.locator('[data-path="servers.ui.model"] option')).toHaveText(["model · not served", "alpha-model", "beta-model"]);
  await expect(settings.locator('.profile-row:has(.profile-summary[data-id="ui"]) .profile-state')).toContainText('Model "model" is not served');
});
