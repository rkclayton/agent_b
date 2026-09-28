// Item 2mo (a) and (b'): THE SETTINGS JOURNEY, FIRST, BECAUSE IT IS THE ONE THAT
// FAILED HIM.
//
// An external review put it plainly: "Component suites passed while the user
// workflow failed." Every package suite this product has was green on 2026-09-27
// while the operator could not add or remove a connection. The gates covered slices;
// none of them walked a thing an operator actually does from end to end.
//
// So this walks it, and every step asserts WHAT IS ON SCREEN — not an API response,
// not a configuration file. Nine journeys are named by the item; this is the first,
// and the overlaps rel-1.31.0/W0 measured are named in the release report rather than
// rewritten here: simple task, tool task, follow-up, restart and interruption are
// already carried by the chat acceptance's 72 recorded proofs.
//
// (b): deterministic, against the harness's controlled model server, so a failure is
// a product defect and not a model mood.
import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

import { start } from "../ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "../../tools/removal-guard.mjs";

const run = promisify(execFile);
const repo = resolve(fileURLToPath(new URL("../..", import.meta.url)));
let root;
let harness;

test.beforeAll(async () => {
  root = await mkdtemp(join(tmpdir(), "agentb-settings-journey-"));
  const exe = join(root, "Agent_b.exe");
  await run(join(repo, ".tools", "go", "bin", "go.exe"), ["build", "-o", exe, "./cmd/harness"], { cwd: repo, windowsHide: true });
  harness = await start({ exe, appRoot: repo, data: join(root, "data"), modelIDs: ["journey-model", "second-model"] });
});

test.afterAll(async () => {
  await harness?.stop();
  if (root) removeTreeWithinAllowedRoots(root, [tmpdir()], "settings-journey Playwright cleanup");
});

test("the settings journey: type a host, Test, pick a model, save, chat, close, remove", async () => {
  // Ten steps including a model round trip: the default 30 seconds is the wrong
  // bound for a journey, and a journey cut short reads as a product failure.
  test.setTimeout(180000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await expect(page.locator("#chat-log")).toBeVisible();

  // 1. Open Settings and its Connections section, on screen.
  await page.locator(".shell-settings").click();
  await expect(page.locator("#settings-page")).toBeVisible();
  await page.locator('.settings-nav [data-id="connections"]').click();
  const rowsBefore = await page.locator(".connection-row").count();

  // 2. Add a connection. The row appears and its editor is open.
  await page.locator('[data-action="add-connection"]').click();
  await expect(page.locator(".connection-row")).toHaveCount(rowsBefore + 1);
  const editor = page.locator(".connection-editor");
  await expect(editor).toBeVisible();
  const path = await editor.locator("[data-path$='.base_url']").getAttribute("data-path");
  const id = path.split(".")[1];

  // 3. TYPE A HOST. He types where his server is; the port he typed is the one
  // discovery tries first (item 2nb (a)).
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill(`127.0.0.1:${harness.modelPort}`);

  // 4. Test. On screen: the note names what it found, and the model control becomes
  // a list of what that port serves.
  await page.locator(`.connection-row [data-action="probe"][data-id="${id}"]`).click();
  await expect(page.locator(".connection-editor .discovery-note")).toContainText(`127.0.0.1:${harness.modelPort}`);
  const model = page.locator(`[data-path="connections.${id}.model"]`);
  await expect(model).toHaveJSProperty("tagName", "SELECT");
  await expect(model.locator("option")).toContainText(["journey-model", "second-model"]);

  // Item 2mh (a) crossing this journey: with SEVERAL models offered, Test proposes no
  // name — it cannot know which one this connection is for — so the label is still
  // the generated id at this point. Measured here rather than assumed.
  await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue(id);

  // 5. Pick a model; the label follows it, because the operator has not named it.
  await model.selectOption("second-model");
  await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue("second-model");

  // 6. THE STORED-KEY FIELD ROUND TRIP. What is typed is never read back: the field
  // returns saying a key is stored, and the key itself is not in it.
  const key = editor.locator(`[data-path="connections.${id}.api_key"]`);
  await expect(key).toHaveAttribute("type", "password");
  await key.fill("journey-secret-value");
  await page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`).click();
  await expect(page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`)).toBeDisabled();
  await page.locator('.settings-nav [data-id="about"]').click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  // The editor opens when its row is clicked; clicking an already-open one would
  // collapse it, which is the state this step needs to be in.
  if (!await page.locator(`[data-path="connections.${id}.api_key"]`).count()) {
    await page.locator(`.connection-summary[data-id="${id}"]`).click();
  }
  const reopened = page.locator(`[data-path="connections.${id}.api_key"]`);
  await expect(reopened).toBeVisible();
  await expect(reopened).toHaveValue("");
  await expect(reopened).toHaveAttribute("placeholder", /leave empty to keep the stored key/);
  await expect(page.locator(".connection-editor .control-note").first()).toHaveText("stored");

  // 7. Start a chat on it. Closing Settings returns to the chat, and the switcher
  // moves this chat onto the connection just made.
  await page.locator(".shell-settings").click();
  await expect(page.locator("#settings-page")).toBeHidden();
  await page.locator(".shell-session-title").click();
  await page.locator(".shell-connection-choice", { hasText: "second-model" }).first().click();
  await expect(page.locator(".shell-session-title")).toContainText("second-model");

  // 8. Send one message and see the answer on screen, from the controlled server.
  await page.locator("#chat-task").fill("journey: say something");
  await page.locator("#chat-send").click();
  await expect(page.locator("#chat-log")).toContainText("ui harness reply", { timeout: 60000 });

  // 9. Close the chat. Item 2ms: the window does not keep showing it.
  const session = await page.locator(".agent-tab-wrap.selected").getAttribute("data-session");
  await page.locator(`.agent-tab-wrap[data-session="${session}"] .agent-tab-close`).click();
  await expect(page.locator(`.agent-tab-wrap[data-session="${session}"]`)).toHaveCount(0);

  // 10. Remove the connection, through the anchored confirmation (item 2l4), and
  // watch the row go.
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${id}"]`).click();
  const popover = page.locator(".confirm-popover");
  await expect(popover).toBeVisible();
  await popover.locator('[data-action="confirm-proceed"]').click();

  // 11. AND THE REFUSAL IS PART OF THE JOURNEY. Choosing this connection in the
  // switcher bound the agent's B role to it, so removing it is refused - on the row
  // that was clicked, naming what to do about it (items 2mb (b), 2nc (c) and (d)).
  const row = page.locator(`.connection-row:has(.connection-summary[data-id="${id}"])`);
  await expect(row).toContainText(/assigned to .* B role/);
  await expect(row).toContainText("Agents page");

  // 12. So he does what it says, and then it goes.
  await page.locator('.settings-nav [data-id="agents"]').click();
  await expect(page.locator("#panel-roles")).toBeVisible();
  // Any connection but the one being removed.
  const other = await page.locator('#panel-roles select[data-role="b"] option').evaluateAll((options, connection) =>
    options.map((option) => option.value).find((value) => value && value !== connection), id);
  await page.locator('#panel-roles select[data-role="b"]').selectOption(other);
  await expect(page.locator('#panel-roles select[data-role="b"]')).toHaveValue(other);
  await page.waitForTimeout(1200);
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator(`.connection-row [data-action="remove-connection"][data-id="${id}"]`).click();
  await expect(page.locator(".confirm-popover")).toBeVisible();
  await page.locator('.confirm-popover [data-action="confirm-proceed"]').click();
  await expect(page.locator(`.connection-summary[data-id="${id}"]`)).toHaveCount(0);
  await expect(page.locator(".connection-row")).toHaveCount(rowsBefore);
});

// Item 2nc (c), carried into the journey: the confirmation belongs to its control, and
// the sheet scrolls. A popover that opens at the top of a scrolled sheet points at
// nothing, and the operator has to guess what he is confirming.
test("the confirmation popover stays with its control on a scrolled sheet", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  // Enough rows that the sheet scrolls.
  await page.setViewportSize({ width: 1250, height: 600 });
  for (let index = 0; index < 6; index++) {
    await page.locator('[data-action="add-connection"]').click();
  }
  // Scrolled by the control the operator would be reaching for, and read back in the
  // same evaluation, because a render restores this scroller's position.
  const scrolled = await page.evaluate(() => {
    const content = document.querySelector(".settings-content");
    const last = [...document.querySelectorAll('[data-action="remove-connection"]')].pop();
    last.scrollIntoView({ block: "end" });
    content.scrollTop = content.scrollHeight;
    return content.scrollTop;
  });
  expect(scrolled, "the sheet did not scroll, so this case proves nothing").toBeGreaterThan(0);

  await page.locator('.connection-row [data-action="remove-connection"]').last().click();
  const popover = page.locator(".confirm-popover");
  await expect(popover).toBeVisible();
  const geometry = await page.evaluate(() => {
    const anchor = [...document.querySelectorAll('[data-action="remove-connection"]')].pop().getBoundingClientRect();
    const box = document.querySelector(".confirm-popover").getBoundingClientRect();
    return {
      anchorTop: anchor.top, boxTop: box.top, boxBottom: box.bottom,
      visible: box.top >= 0 && box.bottom <= window.innerHeight,
    };
  });
  // Anchored: within a row's height of the thing it is asking about, and on screen
  // rather than scrolled out of it.
  expect(Math.abs(geometry.boxTop - geometry.anchorTop), JSON.stringify(geometry)).toBeLessThan(120);
  expect(geometry.visible, JSON.stringify(geometry)).toBe(true);
  await page.locator('.confirm-popover [data-action="confirm-cancel"]').click();
  await expect(popover).toHaveCount(0);
});
