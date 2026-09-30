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
import { mkdir, mkdtemp } from "node:fs/promises";
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

test("Entra credentials show account controls and configuration without defaults", async () => {
	const page = await harness.context.newPage();
	await page.route("**/api/credentials", async (route) => route.fulfill({
		status: 200,
		contentType: "application/json",
		body: JSON.stringify({ credentials: [{ name: "work-api", kind: "entra", origin: "https://api.example.test", stored_at: "2026-09-29T00:00:00Z", account: "person@example.test", sign_in_needed: false }] }),
	}));
	await page.goto(`${harness.base}/chat`);
	await page.locator(".shell-settings").click();
	await page.locator('.settings-nav [data-id="shell"]').click();
	await expect(page.getByText("signed in as person@example.test")).toBeVisible();
	await expect(page.locator('[data-action="credential-sign-in"][data-id="work-api"]')).toHaveText("Switch account");
	await expect(page.locator('[data-action="credential-device-code"][data-id="work-api"]')).toBeVisible();
	await expect(page.locator('[data-action="credential-sign-out"][data-id="work-api"]')).toBeVisible();
	await page.getByText("Add Microsoft Entra").click();
	for (const selector of ["#credential-entra-name", "#credential-entra-origin", "#credential-tenant", "#credential-client-id", "#credential-scopes"]) {
		await expect(page.locator(selector)).toHaveValue("");
	}
	const evidence = join(repo, "candidate", "rel-1.44.0-w7-entra", "settings-credentials.png");
	await mkdir(join(repo, "candidate", "rel-1.44.0-w7-entra"), { recursive: true });
	await page.screenshot({ path: evidence, fullPage: true });
	await page.close();
});

test("Phone shows all four wire states and follows presence without a reload", async () => {
	const page = await harness.context.newPage();
	let status = { state: "not paired" };
	await page.route("**/api/broker/status", async (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(status) }));
	const evidence = join(repo, "candidate", "rel-1.45.0-w1-phone");
	await mkdir(evidence, { recursive: true });
	await page.goto(`${harness.base}/chat`);
	await page.locator(".shell-settings").click();
	await page.locator('.settings-nav [data-id="shell"]').click();

	const cases = [[{ state: "not paired" }, "NOT PAIRED", "not-paired.png"], [{ state: "broker unreachable", paired_device: "phone", next_attempt_at: "2026-09-29T22:30:00Z", ended_reason: "network: dial refused" }, "PAIRED — BROKER UNREACHABLE", "broker-unreachable.png"], [{ state: "holding", paired_device: "phone" }, "PAIRED — HOLDING, PHONE NOT CONNECTED", "holding.png"], [{ state: "phone connected", paired_device: "phone", last_message_at: "2026-09-29T22:31:00Z" }, "PAIRED — PHONE CONNECTED", "phone-connected.png"]];
	for (const [next, label, file] of cases) {
		status = next;
		const renderedState = page.locator(".account-status").filter({ hasText: label }).first();
		await expect(renderedState).toBeVisible({ timeout: 5000 });
		await renderedState.scrollIntoViewIfNeeded();
		await page.screenshot({ path: join(evidence, file), fullPage: true });
	}
	await expect(page.locator(".account-status").filter({ hasText: "last phone message 2026-09-29T22:31:00Z" }).first()).toBeVisible();
	await page.close();
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

// Item 2nn's acceptance, cases 3, 5 and 6, on screen, because every one of the three
// is about what the operator sees and none of them can be proved from a response.

// 3: "Rename only, Save: saved, row shows the new label, no Test run, no refusal."
// This is the one he reported as a refusal and W0 found was a dead button: the save
// carried the disabled attribute from the last render and no request was ever made.
test("a rename alone saves, with no Test and no refusal", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  const posts = [];
  page.on("request", (request) => { if (request.method() === "POST" || request.method() === "PATCH") posts.push(request.url()); });
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  posts.length = 0;

  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed by hand");
  const save = page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`);
  // The button is LIVE the moment there is something to save — no Test first.
  await expect(save).toBeEnabled();
  await save.click();
  await expect(save).toBeDisabled();
  await expect(page.locator(`.connection-summary[data-id="${id}"]`)).toContainText("renamed by hand");
  await expect(page.locator(".connection-refusal")).toHaveCount(0);
  expect(posts.some((url) => url.includes("/api/config")), `no save request was made: ${JSON.stringify(posts)}`).toBe(true);
  expect(posts.some((url) => url.includes("/probe")), "a rename ran a test").toBe(false);
  await page.close();
});

// 5: "During Test the bar is visible in a screenshot taken 100 ms after the click,
// with a phase line; the DOM never contains the bare text 'testing'." The address is
// a TEST-NET-1 one (RFC 5737), which nothing answers, so the walk is still running
// when the screenshot is taken.
test("the bar is on screen 100 ms after Test, and the bare word is not", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill("192.0.2.1:8080");

  await page.locator(`.connection-row [data-action="probe"][data-id="${id}"]`).click();
  await page.waitForTimeout(100);
  // The viewport rather than the row: the list re-renders while the walk runs, and an
  // element screenshot of a node that was just replaced fails for a reason that has
  // nothing to do with what is being proved.
  const shot = await page.screenshot();
  expect(shot.length, "no screenshot was taken").toBeGreaterThan(0);
  const seen = await page.evaluate((connection) => {
    const seat = document.querySelector(`[data-probe-wait="${connection}"] .wait`);
    return {
      bar: !!seat,
      cells: seat ? seat.querySelectorAll(".wait-cell").length : 0,
      line: seat ? seat.querySelector(".wait-line").textContent.trim() : "",
      word: /(^|[^a-z])testing([^a-z]|$)/i.test(document.body.innerText),
    };
  }, id);
  expect(seen.bar, "there is no waiting element in the control that was pressed").toBe(true);
  expect(seen.cells).toBeGreaterThan(0);
  expect(seen.line, "the bar says nothing about what is being waited for").toContain("192.0.2.1:8080");
  expect(seen.word, "the bare word is still on screen").toBe(false);
  await page.close();
});

// 6: "The sheet's field order is label, address, key, Test, model, Evaluate, Save;
// Advanced is collapsed and below Save."
test("the sheet reads top to bottom as the flow", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];

  const order = async () => page.evaluate((connection) => {
    const marks = [
      [`[data-path="connections.${connection}.label"]`, "label"],
      [`[data-path="connections.${connection}.base_url"]`, "address"],
      [`[data-path="connections.${connection}.api_key"]`, "key"],
      [`.connection-editor [data-action="probe"][data-id="${connection}"]`, "Test"],
      [`[data-path="connections.${connection}.model"]`, "model"],
      [`.connection-editor [data-action="measure-connection"][data-id="${connection}"]`, "Evaluate"],
      [`.connection-editor [data-action="save-connection"][data-id="${connection}"]`, "Save"],
      [".connection-advanced", "Advanced"],
    ];
    return marks
      .map(([selector, name]) => [name, document.querySelector(selector)])
      .filter(([, node]) => node)
      .map(([name, node]) => ({ name, top: node.getBoundingClientRect().top }))
      .sort((a, b) => a.top - b.top)
      .map((entry) => entry.name);
  }, id);

  // Before a model is chosen, Evaluate is not among them: nothing but the flow is
  // on screen until the connection has one.
  expect(await order()).toEqual(["label", "address", "key", "Test", "model", "Save", "Advanced"]);
  await expect(page.locator(".connection-advanced")).not.toHaveAttribute("open", /.*/);

  // With a model chosen, Evaluate takes its place between the model and Save.
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill(`127.0.0.1:${harness.modelPort}`);
  await page.locator(`.connection-row [data-action="probe"][data-id="${id}"]`).click();
  await expect(page.locator(`[data-path="connections.${id}.model"]`)).toHaveJSProperty("tagName", "SELECT");
  await page.locator(`[data-path="connections.${id}.model"]`).selectOption("journey-model");
  await expect(page.locator(`.connection-editor [data-action="measure-connection"][data-id="${id}"]`)).toBeVisible();
  expect(await order()).toEqual(["label", "address", "key", "Test", "model", "Evaluate", "Save", "Advanced"]);
  await page.close();
});

// Item 2nq (c) and (d): HIS PICK SURVIVES TEST AND SAVE, and it is VISIBLE.
//
// "i select a different model and press test it goes back to MODEL. i save and it goes
// back to MODEL." W0 reproduced it on a stub and found the pick was never lost: the
// model picker rendered the SAVED value and never the draft, so every render after a
// choice — and Test is a render — drew the old value over his. The field lied.
test("the model he picks is the one the field shows, through Test and through Save", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill(`127.0.0.1:${harness.modelPort}`);
  await page.locator(`.connection-row [data-action="probe"][data-id="${id}"]`).click();

  const model = page.locator(`[data-path="connections.${id}.model"]`);
  await expect(model).toHaveJSProperty("tagName", "SELECT");
  await expect(model.locator("option")).toContainText(["journey-model", "second-model"]);

  // Pick the one that is NOT the first, which is what Test proposes.
  await model.selectOption("second-model");
  await expect(model, "the field forgot the pick as soon as it was made").toHaveValue("second-model");

  // Test again: the field still shows what he picked, not what is saved.
  await page.locator(`.connection-row [data-action="probe"][data-id="${id}"]`).click();
  await expect(page.locator(".connection-editor .discovery-note")).toBeVisible();
  await expect(model, "Test drew the saved model over his pick").toHaveValue("second-model");

  // Save writes it, and the field still shows it afterwards.
  await page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`).click();
  await expect(page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`)).toBeDisabled();
  await expect(model).toHaveValue("second-model");
  const saved = await page.evaluate(async () => (await (await fetch("/api/config")).json()).connections);
  const written = saved.find((connection) => connection.id === id);
  expect(written?.model, "the saved configuration does not hold the picked model").toBe("second-model");
  await page.close();
});

// (d) and (e): a refused Save keeps every draft on screen and says which connection and
// which field it is about.
test("a refused save keeps the drafts and names the connection", async () => {
  test.setTimeout(120000);
  const page = await harness.context.newPage();
  await page.goto(`${harness.base}/chat`);
  await page.locator(".shell-settings").click();
  await page.locator('.settings-nav [data-id="connections"]').click();
  await page.locator('[data-action="add-connection"]').click();
  const editor = page.locator(".connection-editor");
  const id = (await editor.locator("[data-path$='.base_url']").getAttribute("data-path")).split(".")[1];
  await editor.locator(`[data-path="connections.${id}.label"]`).fill("renamed while refused");
  await editor.locator(`[data-path="connections.${id}.base_url"]`).fill("127.0.0.1:9");

  // The literal placeholder is not a model, so the server refuses to write it back —
  // which is exactly what kept it alive on the operator's disk.
  await page.evaluate((connection) => {
    const node = document.querySelector(`[data-path="connections.${connection}.model"]`);
    node.value = "";
    const option = document.createElement("option");
    option.value = "model";
    option.textContent = "model";
    node.append(option);
    node.value = "model";
    // Both, the way a real pick does: the input listener records the draft and the
    // change listener is what fills the rest from the chosen model.
    node.dispatchEvent(new Event("input", { bubbles: true }));
    node.dispatchEvent(new Event("change", { bubbles: true }));
  }, id);
  await page.locator(`.connection-row [data-action="save-connection"][data-id="${id}"]`).click();

  const refusal = page.locator(".connection-refusal, .settings-group .field-error, [data-save-status]");
  await expect(page.locator("[data-save-status]")).toContainText("Save failed");
  // It names the connection, and the row it is about carries it.
  const row = page.locator(`.connection-row:has(.connection-summary[data-id="${id}"])`);
  await expect(row).toContainText("model is empty");
  // Every draft is still on screen: nothing was re-rendered back to the saved value.
  await expect(editor.locator(`[data-path="connections.${id}.label"]`)).toHaveValue("renamed while refused");
  await expect(editor.locator(`[data-path="connections.${id}.base_url"]`)).toHaveValue("127.0.0.1:9");
  void refusal;
  await page.close();
});
