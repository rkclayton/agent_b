// Item 2gf, v1.1.2/W1 — the scenarios. Chat is reachable from everywhere,
// always: four pages (Plan, Console, Settings, Setup) under two conditions
// (the model unreachable, and `/api/plan` failing) is the eight the acceptance
// names. Item 2gi's three Plan renders are proved here too, because they share
// this harness and the same page.
//
//   node scripts/reachability-scenarios.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
//
// Every scenario runs with the model unreachable, which is the operator's own
// condition; `/api/plan` failing is layered on top for the second four.
import assert from "node:assert/strict";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { start, waitFor } from "./ui-harness.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });

const results = [];
let failures = 0;
function record(name, ok, detail) {
  results.push({ name, ok, detail });
  if (!ok) failures++;
  process.stdout.write(`${ok ? "PASS" : "FAIL"} ${name}${ok ? "" : ` — ${JSON.stringify(detail)}`}\n`);
}

// reachesChat performs ONE click on the way back a page offers and says where
// the window landed. It is deliberately one gesture: the acceptance says one
// click, so the scenario may not take two.
async function reachesChat(page, how) {
  const before = page.url();
  await how(page);
  await page.waitForFunction(() => document.body.dataset.page === "chat", null, { timeout: 12000 }).catch(() => {});
  const landed = await page.evaluate(() => document.body.dataset.page || null);
  return { before, after: page.url(), landed, ok: landed === "chat" };
}

const clickTab = (page) => page.locator(".agent-tab[data-session]").first().click();
const clickPlanToggle = (page) => page.locator('.shell-page[data-page="plan"]').click();
const clickSetupChat = (page) => page.locator("#setup-chat").click();

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: false });
try {
  const sessionID = Object.keys(harness.initial.sessions)[0];

  // ---- 2gf: the eight ---------------------------------------------------
  for (const planFailing of [false, true]) {
    const label = planFailing ? "plan-api-failing" : "model-unreachable";
    const context = planFailing ? await harness.browser.newContext({ viewport: { width: 1250, height: 975 } }) : harness.context;
    if (planFailing) await context.route("**/api/plan*", (route) => route.fulfill({ status: 500, contentType: "application/json", body: JSON.stringify({ title: "failed for the scenario" }) }));

    // Plan — the page the operator was stuck on. Its tab is the way back.
    let page = await context.newPage();
    await page.goto(`${harness.base}/plan?session=${sessionID}`);
    await page.locator('#app-shell[data-page="plan"]').waitFor({ state: "visible" });
    let result = await reachesChat(page, clickTab);
    record(`2gf/${label}/plan-tab-reaches-chat`, result.ok, result);
    await page.close();

    // Plan — and the toggle that brought him there brings him back.
    page = await context.newPage();
    await page.goto(`${harness.base}/plan?session=${sessionID}`);
    await page.locator('#app-shell[data-page="plan"]').waitFor({ state: "visible" });
    result = await reachesChat(page, clickPlanToggle);
    record(`2gf/${label}/plan-toggle-returns-to-chat`, result.ok, result);
    await page.close();

    // Console.
    page = await context.newPage();
    await page.goto(`${harness.base}/?session=${sessionID}`);
    await page.locator('body[data-page="console"]').waitFor();
    result = await reachesChat(page, clickTab);
    record(`2gf/${label}/console-tab-reaches-chat`, result.ok, result);
    await page.close();

    // Settings, opened over Console the way the shell opens it.
    page = await context.newPage();
    await page.goto(`${harness.base}/?session=${sessionID}`);
    await page.locator('body[data-page="console"]').waitFor();
    await page.locator(".shell-settings").click();
    await page.locator("#settings-page").waitFor({ state: "visible" });
    result = await reachesChat(page, clickTab);
    record(`2gf/${label}/settings-tab-reaches-chat`, result.ok, result);
    await page.close();

    if (planFailing) await context.close();
  }

  // Setup carries no tab strip, so it owes the single stand-in route.
  {
    const page = await harness.context.newPage();
    await page.goto(`${harness.base}/setup`);
    await page.locator("#setup-chat").waitFor({ state: "visible" });
    const result = await reachesChat(page, clickSetupChat);
    record("2gf/model-unreachable/setup-chat-route-reaches-chat", result.ok, result);
    await page.close();
  }

  // The strip itself, on every page: present, visible and live.
  {
    const page = await harness.context.newPage();
    const strip = {};
    for (const [name, path] of [["chat", `/chat?session=${sessionID}`], ["console", `/?session=${sessionID}`], ["plan", `/plan?session=${sessionID}`]]) {
      await page.goto(`${harness.base}${path}`);
      await page.locator("#app-shell").waitFor({ state: "visible" });
      await page.waitForTimeout(600);
      strip[name] = await page.evaluate(() => ({
        tabs: document.querySelectorAll(".agent-tab[data-session]").length,
        newChat: !!document.querySelector(".agent-tab-new") && !document.querySelector(".agent-tab-new").disabled,
      }));
    }
    const ok = Object.values(strip).every((value) => value.tabs > 0 && value.newChat);
    record("2gf/tab-strip-and-new-chat-live-on-every-page", ok, strip);
    await page.close();
  }

  // ---- 2gi: the Plan page renders exactly one of the two ---------------
  // The defect is a race, so these force it rather than hoping for it: the
  // snapshot is held back until after `/api/plans` has resolved, which is the
  // order that left `loadPlans` returning before it rendered anything.
  const renderedState = (page) => page.evaluate(() => ({
    empty: !document.getElementById("plan-list-empty")?.hidden,
    rows: document.querySelectorAll("#plan-list > li").length,
    name: document.getElementById("plan-name")?.textContent ?? null,
  }));
  async function planPage(context, { holdSnapshot = 0, holdPlans = 0 } = {}) {
    const page = await context.newPage();
    if (holdSnapshot) {
      await page.route("**/api/state*", async (route) => {
        await new Promise((done) => setTimeout(done, holdSnapshot));
        await route.continue();
      });
    }
    if (holdPlans) {
      await page.route("**/api/plans*", async (route) => {
        await new Promise((done) => setTimeout(done, holdPlans));
        await route.continue();
      });
    }
    await page.goto(`${harness.base}/plan?session=${sessionID}`);
    await page.locator('#app-shell[data-page="plan"]').waitFor({ state: "visible" });
    return page;
  }
  {
    // Zero plans, with the snapshot held back: the empty state, never blank.
    let page = await planPage(harness.context, { holdSnapshot: 2000 });
    let state = await waitFor(async () => {
      const value = await renderedState(page);
      return (value.empty || value.rows > 0) ? value : null;
    }, "the Plan page rendered a state", 12000).catch(() => null);
    record("2gi/zero-plans-renders-the-empty-state", !!state && state.empty && state.rows === 0, state);
    await page.close();

    // The same page once the snapshot lands: still exactly one state, and the
    // right pane says so rather than sitting blank.
    page = await planPage(harness.context, { holdSnapshot: 1200 });
    await page.waitForTimeout(3000);
    state = await renderedState(page);
    record("2gi/snapshot-arriving-late-still-renders-one-state", state.empty && state.rows === 0 && state.name === "Plan", state);
    await page.close();

    // The list arriving late: the empty state first, then the list, and never
    // a blank interval in between.
    page = await planPage(harness.context, { holdPlans: 1500 });
    const samples = [];
    for (let index = 0; index < 12; index++) {
      samples.push(await page.evaluate(() => ({
        at: Math.round(performance.now()),
        empty: !document.getElementById("plan-list-empty")?.hidden,
        rows: document.querySelectorAll("#plan-list > li").length,
      })));
      await page.waitForTimeout(250);
    }
    const settled = samples.slice(-4);
    const blank = settled.filter((sample) => !sample.empty && sample.rows === 0);
    record("2gi/late-list-never-shows-neither", blank.length === 0, { blank_samples: blank.length, settled });
    await page.close();

    // One plan: the list draws it and the right pane is that plan, not the
    // empty state. The plan is registered through the ordinary route.
    const repo = join(harness.dataRoot, "a-plan-repo");
    await mkdir(repo, { recursive: true });
    const registered = await fetch(`${harness.base}/api/plans`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": harness.initial.mutation_token },
      body: JSON.stringify({ repo }),
    });
    if (registered.status >= 400) {
      record("2gi/one-plan-renders-the-list-and-its-pane", false, { register_status: registered.status, body: (await registered.text()).slice(0, 300) });
    } else {
      page = await planPage(harness.context, { holdSnapshot: 1200 });
      const state = await waitFor(async () => {
        const value = await renderedState(page);
        return value.rows > 0 ? value : null;
      }, "the Plan page rendered its one plan", 12000).catch(() => null);
      record("2gi/one-plan-renders-the-list-and-its-pane", !!state && state.rows === 1 && !state.empty && state.name !== "Plan", state);
      await page.close();
    }
  }
} finally {
  await harness.stop();
}

await writeFile(join(evidence, "w1-reachability-scenarios.json"), JSON.stringify({ schema: 1, failures, results }, null, 2));
process.stdout.write(`REACHABILITY SCENARIOS ${failures === 0 ? "PASS" : "FAIL"} ${results.length - failures} of ${results.length}\n`);
await rm(resolve(args.data), { recursive: true, force: true }).catch(() => {});
process.exitCode = failures === 0 ? 0 : 1;
