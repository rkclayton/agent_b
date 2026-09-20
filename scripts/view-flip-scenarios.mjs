// Item 2hb, v1.2.4/W1 — the operator's sequence, as a scenario.
//
//   "i went to thoughts then i went to settings and it kicked me to chat, back
//    to thoughts and i'm stuck with the loading across the top"
//   "flipping between thoughts and settings is buggy. it doesn't even go to
//    settings, it goes to chat instead and then it gets stuck loading"
//
// Plan is its own document; the chat and Settings share the other one. So every
// move between them is either a document load or a mount inside one document,
// and the question this asks is only ever: WHICH VIEW IS UP, and does the shell
// agree with itself about it.
//
//   node scripts/view-flip-scenarios.mjs --exe <exe> --app-root <dir> --data <dir> --evidence <dir>
import assert from "node:assert/strict";
import { mkdir, writeFile } from "node:fs/promises";
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

// viewState reads every place a view is recorded at once. The point of the item
// is that these must agree; reading them together is how a disagreement is
// named rather than guessed at.
const viewState = (page) => page.evaluate(() => ({
  path: location.pathname,
  hash: location.hash,
  body: document.body.dataset.page || null,
  shell: document.getElementById("app-shell")?.dataset.page || null,
  settingsOpen: !!document.getElementById("settings-page") && !document.getElementById("settings-page").hidden,
  gearPressed: document.querySelector(".shell-settings")?.getAttribute("aria-pressed") || null,
  chatVisible: !!document.getElementById("chat-task") && !document.getElementById("chat-task").closest("[hidden]"),
  planVisible: !!document.getElementById("plan-list"),
  // The progress the operator saw "across the top": whatever the shell is
  // reporting as in flight, wherever it is drawn.
  progress: [...document.querySelectorAll("[data-progress], .shell-progress, #connection")].map((node) => node.textContent.trim()).filter(Boolean),
  mountMS: window.__agentbViewMount?.ms ?? null,
  mountView: window.__agentbViewMount?.view ?? null,
}));

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: args.data, reachable: true });
try {
  const page = await harness.context.newPage();
  const pageErrors = [];
  page.on("pageerror", (error) => pageErrors.push({ at: page.url(), message: error.message, stack: String(error.stack || "").split(/\r?\n/).slice(0, 4).join(" | ") }));
  page.on("console", (message) => { if (message.type() === "error") pageErrors.push({ at: page.url(), console: message.text() }); });

  // A chat to carry in the strip, so every page has a session in its links.
  const created = await page.request.post(`${harness.base}/api/sessions`, { data: { agent_id: "ui" }, headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": harness.initial.mutation_token } });
  assert.ok(created.ok(), `creating a chat: ${created.status()}`);
  const sessionID = (await created.json()).id;

  // 1. The operator's first move: Plan, then the gear.
  await page.goto(`${harness.base}/plan?session=${sessionID}`);
  await waitFor(async () => (await viewState(page)).planVisible, "the Plan page drew");
  const onPlan = await viewState(page);
  await page.locator(".shell-settings").click();
  // The gear on Plan is a link to the other document, so this is a real
  // navigation: wait for it to finish, and for the shell to have drawn
  // SOMETHING, before asking which view is up. Reading at the moment the URL
  // changes reads a document that has not run its scripts yet.
  await page.waitForLoadState("load");
  await waitFor(async () => {
    const state = await viewState(page);
    return state.settingsOpen || state.chatVisible ? state : null;
  }, "the gear landed somewhere", 10000).catch(() => null);
  const afterGear = await viewState(page);
  record("gear-from-plan-opens-settings", afterGear.settingsOpen, { onPlan, afterGear, pageErrors: pageErrors.slice(0, 4) });

  // 2. Closing Settings returns to the view it was opened from.
  await page.locator(".shell-settings").click();
  await waitFor(async () => !(await viewState(page)).settingsOpen, "Settings closed");
  const afterClose = await viewState(page);
  record("settings-closes-to-the-view-it-opened-from", afterClose.planVisible, { afterClose });

  // 3. Nothing is left claiming to be in flight.
  const settled = await waitFor(async () => {
    const state = await viewState(page);
    return state.progress.length === 0 ? state : null;
  }, "no progress line lingers", 8000).catch(async () => ({ ...(await viewState(page)), lingered: true }));
  record("no-progress-line-lingers", !settled.lingered, settled);

  // 4. Twenty flips: Plan -> Settings -> Plan -> chat -> Plan, with Escape and
  //    back/forward mixed in. The expected view is up every time.
  const flips = [];
  for (let round = 0; round < 20; round += 1) {
    await page.goto(`${harness.base}/plan?session=${sessionID}`);
    await waitFor(async () => (await viewState(page)).planVisible, `round ${round}: Plan`);
    const started = Date.now();
    await page.locator(".shell-settings").click();
    await waitFor(async () => (await viewState(page)).settingsOpen, `round ${round}: Settings`);
    // Two different numbers, and only one of them is this application's to
    // budget: the round trip includes a document load, which is the browser's
    // time; the mount is the work the shell does to put the view up.
    const roundTripMS = Date.now() - started;
    const mount = await page.evaluate(() => window.__agentbViewMount || null);
    if (round % 2 === 0) await page.keyboard.press("Escape");
    else await page.locator(".shell-settings").click();
    await waitFor(async () => !(await viewState(page)).settingsOpen, `round ${round}: Settings closed`);
    const closed = await viewState(page);
    const chatStarted = Date.now();
    await page.goto(`${harness.base}/chat?session=${sessionID}`);
    await waitFor(async () => (await viewState(page)).chatVisible, `round ${round}: chat`);
    const chatMS = Date.now() - chatStarted;
    await page.goBack();
    await page.goForward();
    const state = await viewState(page);
    flips.push({ round, roundTripMS, mount, chatMS, closedTo: closed.planVisible ? "plan" : closed.chatVisible ? "chat" : "none", state });
  }
  const wrongView = flips.filter((flip) => flip.closedTo !== "plan");
  record("twenty-flips-land-on-the-expected-view", wrongView.length === 0, { wrongView: wrongView.slice(0, 3), rounds: flips.length });
  const missing = flips.filter((flip) => !flip.mount || typeof flip.mount.ms !== "number");
  record("every-flip-reports-its-mount-time", missing.length === 0, { missing: missing.slice(0, 3) });
  const slow = flips.filter((flip) => (flip.mount?.ms ?? Infinity) >= 100);
  record("every-mount-under-100ms", slow.length === 0, {
    slow: slow.slice(0, 5).map((flip) => ({ round: flip.round, mount: flip.mount })),
    maxMount: Math.max(...flips.map((flip) => flip.mount?.ms ?? 0)),
    maxRoundTrip: Math.max(...flips.map((flip) => flip.roundTripMS)),
  });
  const lingering = flips.filter((flip) => flip.state.progress.length);
  record("no-progress-after-twenty-flips", lingering.length === 0, { lingering: lingering.slice(0, 3) });

  await writeFile(join(evidence, "view-flips.json"), `${JSON.stringify({ onPlan, afterGear, afterClose, settled, flips, results }, null, 1)}\n`);
} finally {
  await harness.stop();
}

process.stdout.write(`${failures ? "VIEW FLIP FAIL" : "VIEW FLIP PASS"} ${results.length - failures} of ${results.length}\n`);
process.exitCode = failures ? 1 : 0;
