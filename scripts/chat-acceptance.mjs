import assert from "node:assert/strict";
import { spawn, spawnSync } from "node:child_process";
import { createServer } from "node:http";
import { mkdir, readFile, readdir, writeFile } from "node:fs/promises";
import { basename, join } from "node:path";
import { chromium } from "playwright";
import { agentStates, assertPageStyleBoundary, provePageStyleBoundaryControl } from "./page-style-boundary.mjs";
import { LIVE_VALUES, OTHER_CHAT_STATE, captureWithMasks } from "./screenshot-masks.mjs";
import { decodePNG } from "./png.mjs";

const args = Object.fromEntries(Array.from({ length: Math.floor(process.argv.slice(2).length / 2) }, (_, index) => {
  const offset = index * 2 + 2;
  return [process.argv[offset].replace(/^--/, ""), process.argv[offset + 1]];
}));
for (const key of ["app", "data", "workspace", "evidence"]) assert.ok(args[key], `missing --${key}`);
const realModel = !!args["real-model-url"];
// The release gate runs headless (item 2el): a locked or unattended desktop
// cannot deliver real input to a visible window, and nothing here measures
// paint. --headless false is for looking at a run, never the release path.
const headless = args.headless !== "false";
const startedAt = Date.now();
const scenarios = [];
const children = [];
let browser;
let edgeContext;
let page;
let shellFlipEvidence;
let composerFamilyEvidence;
let shellStyleBoundaryEvidence;
let app;
let model;
let modelPort;
let planningBriefOriginal = "";
let releaseQueue = null;
let releaseBusy = null;
let slowAccountingArmed = false;
const slowAccountingTrace = [];
const terminateChildren = () => {
  try { model?.closeAllConnections?.(); } catch {}
  for (const child of [...children].reverse()) { try { child.kill(); } catch {} }
};
process.on("exit", terminateChildren);

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const waitForChildExit = (child, timeout = 5000) => child?.exitCode !== null
  ? Promise.resolve()
  : Promise.race([new Promise((resolve) => child.once("exit", resolve)), sleep(timeout)]);
const record = (name) => { scenarios.push(name); process.stdout.write(`PASS ${name}\n`); };
const freePort = async () => {
  const probe = createServer();
  await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
  const port = probe.address().port;
  await new Promise((resolve) => probe.close(resolve));
  return port;
};
const stream = (response, delta, finish = "stop") => {
  response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
  response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: finish }], usage: { prompt_tokens: response.agentbPromptTokens || 120, completion_tokens: 12, prompt_tokens_details: { cached_tokens: 80 } } })}\n\ndata: [DONE]\n\n`);
};
const latestUser = (body) => [...(body.messages || [])].reverse().find((message) => message.role === "user")?.content || "";
const hasToolAfterLatestUser = (body) => {
  const messages = body.messages || [];
  const index = messages.findLastIndex((message) => message.role === "user");
  return messages.slice(index + 1).some((message) => message.role === "tool");
};
const toolCountAfterLatestUser = (body) => {
  const messages = body.messages || [];
  const index = messages.findLastIndex((message) => message.role === "user");
  return messages.slice(index + 1).filter((message) => message.role === "tool").length;
};
const fakeHandler = async (request, response) => {
	if (request.url === "/agentb-release/latest") {
		const base = `http://${request.headers.host}`;
		response.setHeader("Content-Type", "application/json");
		return void response.end(JSON.stringify({ tag_name: "v9.9.9", body: "Fixture distribution release\nLocal only.", draft: false, prerelease: false, assets: [
			{ name: "release.json", browser_download_url: `${base}/agentb-release/release.json` },
			{ name: "Agent_b-setup.exe", browser_download_url: `${base}/agentb-release/Agent_b-setup.exe` },
		] }));
	}
  if (request.url === "/arm-slow-accounting") {
    slowAccountingArmed = true;
    slowAccountingTrace.push({ action: "armed", at: Date.now() });
    return void response.end(JSON.stringify({ armed: true }));
  }
  if (request.url === "/props") return void response.end(JSON.stringify({ server: "agentb-fake", n_ctx: 32768 }));
  if (request.url === "/v1/models") return void response.end(JSON.stringify({ data: [{ id: "agentb-fake" }] }));
  let raw = "";
  for await (const chunk of request) raw += chunk;
  const body = raw ? JSON.parse(raw) : {};
  response.agentbPromptTokens = Math.max(1, Math.ceil(JSON.stringify(body.messages || []).length / 4));
  // User submission tokenizes before the supervised run starts. Delay the
  // run's template request instead so the connected-accounting fallback and
  // model.busy event are exercised on every trial, independent of token-cost caches.
  if (slowAccountingArmed && request.url === "/apply-template" && (body.messages || []).some((message) => String(message.content || "").includes("acceptance: slow accounting"))) {
    slowAccountingArmed = false;
    slowAccountingTrace.push({ action: "delayed", url: request.url, at: Date.now() });
    await sleep(4000);
  }
  if (request.url === "/tokenize") {
    const content = String(body.content || body.prompt || "");
    return void response.end(JSON.stringify({ tokens: Array.from({ length: Math.max(1, Math.ceil(content.length / 4)) }, (_, i) => i) }));
  }
  if (request.url === "/apply-template") {
    if (JSON.stringify(body.messages || []).includes('"image_url"')) {
      response.statusCode = 400;
      return void response.end("image input is not supported by the acceptance profile");
    }
    let historyStarted = false;
    const invalid = (body.messages || []).findIndex((message, index) => {
      const system = ["system", "developer"].includes(message.role);
      const rejected = index > 0 && system && historyStarted;
      if (!system) historyStarted = true;
      return rejected;
    });
    if (invalid >= 0) {
      response.statusCode = 500;
      return void response.end("System message must be at the beginning.");
    }
    return void response.end(JSON.stringify({ prompt: JSON.stringify(body.messages || []) }));
  }
  if (request.url !== "/v1/chat/completions") { response.statusCode = 404; return void response.end(); }
  const user = latestUser(body);
  if (user.includes("Summarize the work so far")) {
    const slowAccounting = (body.messages || []).some((message) => String(message.content || "").includes("acceptance: slow accounting"));
    response.setHeader("Content-Type", "application/json");
    const content = slowAccounting
      ? "acceptance: slow accounting completed read; answer the pending request now."
      : "Earlier acceptance steps completed; keep the stable system and tool prefix.";
    return void response.end(JSON.stringify({ choices: [{ message: { content }, finish_reason: "stop" }], usage: { prompt_tokens: 300, completion_tokens: 18, prompt_tokens_details: { cached_tokens: 200 } } }));
  }
  // Item 2fg: the walk's step 3 — a 60 s tool the operator stops.
  if (user.includes("acceptance: stop mid tool") && !hasToolAfterLatestUser(body)) {
    return stream(response, { tool_calls: [{ index: 0, id: "stop-mid-tool", type: "function", function: { name: "shell", arguments: JSON.stringify({ command: "Start-Sleep -Seconds 60; Write-Output walk-slept", timeout_s: 120 }) } }] }, "tool_calls");
  }
  if (user.includes("acceptance: sent while stopping")) return stream(response, { content: "Held message answered." });
  if (user.includes("acceptance: resume after stop")) return stream(response, { content: "Resumed after the stop." });
  if (user.includes("acceptance: stop")) return;
	if (user.includes("acceptance: inbox stop") && !hasToolAfterLatestUser(body)) {
		// v1.2.2/W3: the stop is written as soon as this request arrives, and at
		// half a second it raced this tool call - the transcript kept a Steps row
		// in some runs and not in others, which moved every capture taken after
		// it. Holding the call lets the mailbox stop always land first, which is
		// what the case is about; if the stop is ever missed, the call still
		// comes and the case still fails.
		await sleep(2500);
		return stream(response, { tool_calls: [{ index: 0, id: "inbox-list", type: "function", function: { name: "list_dir", arguments: JSON.stringify({ path: ".", depth: 1 }) } }] }, "tool_calls");
	}
	if (user.includes("acceptance: inbox stop")) return stream(response, { content: "INBOX STOP was missed." });
  if (user.includes("acceptance: queue leader")) {
    await new Promise((resolve) => { releaseQueue = resolve; response.on("close", resolve); });
    return stream(response, { content: "Queue leader completed." });
  }
  if (user.includes("acceptance: prose stream")) {
    response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    response.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "VISIBLE PARTIAL" }, finish_reason: null }] })}\n\n`);
    await sleep(700);
    response.end(`data: ${JSON.stringify({ choices: [{ delta: { content: " COMPLETE" }, finish_reason: "stop" }], usage: { prompt_tokens: response.agentbPromptTokens || 120, completion_tokens: 12, prompt_tokens_details: { cached_tokens: 80 } } })}\n\ndata: [DONE]\n\n`);
    return;
  }
  if (user.includes("acceptance: scratch file")) {
    if (!hasToolAfterLatestUser(body)) return stream(response, { tool_calls: [{ index: 0, id: "scratch-write", type: "function", function: { name: "write_file", arguments: JSON.stringify({ path: "scratch-proof.txt", content: "scratch tool passed\n" }) } }] }, "tool_calls");
    return stream(response, { content: "SCRATCH FILE COMPLETE" });
  }
  if (user.includes("acceptance: menu stream")) {
    const count = toolCountAfterLatestUser(body);
    // v1.2.2/W3: the first two calls read a file that is not there and fail,
    // and the scenario stops this run by hand somewhere after them. Holding
    // the third response puts the stop inside a window wide enough that both
    // failed reads, and only those two, are in every run: two captures of one
    // build differed by nothing else. The hold ends when the stop closes the
    // request, so nothing is ever written to a socket that has gone.
    if (count < 2) await sleep(1000);
    else {
      await new Promise((resolve) => {
        const timer = setTimeout(resolve, 20000);
        response.on("close", () => { clearTimeout(timer); resolve(); });
      });
      if (response.destroyed || response.writableEnded) return;
    }
    if (count < 8) {
      const path = count < 2 ? "long-tool.txt" : "AGENTS.md";
      return stream(response, { tool_calls: [{ index: 0, id: `menu-stream-${count}`, type: "function", function: { name: "read_file", arguments: JSON.stringify({ path }) } }] }, "tool_calls");
    }
    return stream(response, { content: "Menu stream completed." });
  }
  // Item 2fc: one chat holds the model while another waits behind it.
  if (user.includes("acceptance: hold the model")) {
    await sleep(4000);
    return stream(response, { content: "HELD ANSWER" });
  }
  if (user.includes("acceptance: queued behind")) return stream(response, { content: "BEHIND ANSWER" });
  // Item 2fs: a run paused on a card nobody answers, and a chat on the same model.
  if (user.includes("acceptance: card nobody answers")) {
    if (!hasToolAfterLatestUser(body)) return stream(response, { tool_calls: [{ index: 0, id: "unanswered-card", type: "function", function: { name: "run_script", arguments: JSON.stringify({ language: "powershell", source: "Write-Output card-answered" }) } }] }, "tool_calls");
    return stream(response, { content: "CARD RELEASED ANSWER" });
  }
  if (user.includes("acceptance: beside the card")) return stream(response, { content: "BESIDE THE CARD ANSWER" });
  if (user.includes("acceptance: live tool")) {
    if (!hasToolAfterLatestUser(body)) {
      await sleep(300);
      const tool = { index: 0, id: "live-slow-shell", type: "function", function: { name: "shell", arguments: JSON.stringify({ command: "Start-Sleep -Milliseconds 5000; Write-Output slow-tool-complete" }) } };
      return stream(response, { tool_calls: [tool] }, "tool_calls");
    }
    return stream(response, { content: "LIVE TOOL COMPLETE" });
  }
  if (user.includes("acceptance: run-script grant")) {
    const count = toolCountAfterLatestUser(body);
    if (count === 0) return stream(response, { tool_calls: [{ index: 0, id: "grant-script-powershell", type: "function", function: { name: "run_script", arguments: JSON.stringify({ language: "powershell", source: "Write-Output first-granted-script" }) } }] }, "tool_calls");
    if (count === 1) return stream(response, { tool_calls: [{ index: 0, id: "grant-script-node", type: "function", function: { name: "run_script", arguments: JSON.stringify({ language: "node", source: "console.log('second-granted-script')" }) } }] }, "tool_calls");
    return stream(response, { content: "RUN SCRIPT SESSION GRANT COMPLETE" });
  }
  if (user.includes("acceptance: busy")) {
    await new Promise((resolve) => { releaseBusy = resolve; response.on("close", resolve); });
    return stream(response, { content: "Busy model resumed." });
  }
  if (user.includes("inspect acceptance directory") && !hasToolAfterLatestUser(body)) {
    return stream(response, { tool_calls: [{ index: 0, id: "acceptance-shell", type: "function", function: { name: "shell", arguments: JSON.stringify({ command: `& "${gitPath}" -C "${bound}" status --short` }) } }] }, "tool_calls");
  }
  if (user.includes("inspect acceptance directory")) return stream(response, { content: "Acceptance answer rendered after the approved shell call." });
  if (user.includes("acceptance: queued follower")) return stream(response, { content: "Queued follower completed." });
  if (user.includes("acceptance: attachment")) return stream(response, { content: "Attachment received and rendered." });
  if (user.includes("acceptance: recovered")) return stream(response, { content: "Recovered after Retry." });
  if (user.includes("acceptance: slow accounting completed read")) return stream(response, { content: "Slow accounting recovered with an estimate." });
  if (user.includes("acceptance: slow accounting") && !hasToolAfterLatestUser(body)) {
    await sleep(700);
    return stream(response, { tool_calls: [{ index: 0, id: "slow-read", type: "function", function: { name: "read_file", arguments: JSON.stringify({ path: "AGENTS.md" }) } }] }, "tool_calls");
  }
  if (user.includes("acceptance: slow accounting")) { await sleep(700); return stream(response, { content: "Slow accounting recovered with an estimate." }); }
  if (user.includes("acceptance: compaction")) return stream(response, { content: `Compaction answer ${"stable ".repeat(180)}` });
  // 2t-ii — the worker's two items. The first finishes cleanly after reading
  // the repository it was given; the second fails three tool calls with
  // DISTINCT arguments, so the cycle guard does not fire before the consecutive
  // tool-error backstop does, while saying the one thing it could not work out.
  if (user.includes("2t worker reads the repository it was given")) {
    if (!hasToolAfterLatestUser(body)) return stream(response, { tool_calls: [{ index: 0, id: "worker-read", type: "function", function: { name: "read_file", arguments: JSON.stringify({ path: "AGENTS.md" }) } }] }, "tool_calls");
    return stream(response, { content: "Read the acceptance rules and made the item true." });
  }
  if (user.includes("2u worker cannot finish this one")) {
    const count = toolCountAfterLatestUser(body);
    return stream(response, { content: "Which database should the cache use?", tool_calls: [{ index: 0, id: `worker-missing-${count}`, type: "function", function: { name: "read_file", arguments: JSON.stringify({ path: `missing-${count}.txt` }) } }] }, "tool_calls");
  }
  if (user.includes("acceptance: plan proposals")) return stream(response, { content: `Plan candidates.\n\`\`\`agentb-plan-proposals\n${JSON.stringify({ version: 1, proposals: [
    { id: "browser-accept", kind: "add", path: "plan.md", old_text: "# Browser plan", new_text: "# Browser plan\n[ ] 2t accepted via tray", item_id: "2t" },
    { id: "browser-dismiss", kind: "reword", path: "plan.md", old_text: "# Browser plan", new_text: "# Dismissed plan", item_id: "2t" },
    { id: "browser-quote", kind: "reword", path: "plan.md", old_text: "# Browser plan", new_text: "# Quoted plan", item_id: "2t" },
  ] })}\n\`\`\`` });
  if (user.includes("<planning-brief>") && user.includes("Acceptance project purpose")) {
    const newText = `${planningBriefOriginal.trimEnd()}\n\n## Purpose\nAcceptance project purpose\n\n## Done\nMilestone one\nMilestone two\nMilestone three\n\n## Boundaries\nDo not touch billing\n`;
    return stream(response, { content: `Drafted plan.md from the supplied scope.\n\`\`\`agentb-plan-proposals\n${JSON.stringify({ version: 1, proposals: [
      { id: "planning-brief-draft", kind: "reword", path: "plan.md", old_text: planningBriefOriginal, new_text: newText, item_id: null },
    ] })}\n\`\`\`` });
  }
  return stream(response, { content: "Acceptance response." });
};
const startFake = async (port = 0) => {
  model = createServer((request, response) => void fakeHandler(request, response).catch((error) => { response.statusCode = 500; response.end(error.stack); }));
  await new Promise((resolve) => model.listen(port, "127.0.0.1", resolve));
  modelPort = model.address().port;
};
const stopFake = async () => {
  if (!model) return;
  const closing = new Promise((resolve) => model.close(resolve));
  model.closeAllConnections?.();
  await closing;
  model = null;
};
const json = async (url, options) => {
  const response = await fetch(url, options);
  const value = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`${response.status} ${JSON.stringify(value)}`);
  return value;
};
// Item 2er: the application's own start (including the signing-state
// inspection it runs before listening) took more than 15 s on a saturated host;
// the wait is for the server answering, with a deadline that load cannot reach.
const waitHTTP = async (url, timeout = 90000) => {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    try { return await json(url); } catch { await sleep(50); }
  }
  throw new Error(`timed out waiting for ${url}`);
};
const waitFileContains = async (path, text, timeout = 12000) => {
	const deadline = Date.now() + timeout;
	while (Date.now() < deadline) {
		try { if ((await readFile(path, "utf8")).includes(text)) return; } catch {}
		await sleep(50);
	}
	throw new Error(`file timeout: ${path} did not contain ${text}`);
};

const browserText = async (selector) => browser.evaluate(`document.querySelector(${JSON.stringify(selector)})?.innerText || ""`);
const projectedChatText = async (sessionID) => JSON.stringify((await state()).sessions[sessionID]?.chat || []);
const waitProjectedChatText = async (sessionID, text, label, timeout = 12000) => {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if ((await projectedChatText(sessionID)).includes(text)) return;
    await sleep(50);
  }
  const session = (await state()).sessions[sessionID];
  throw new Error(`projection timeout: ${label}; run=${JSON.stringify(session?.run || null)}; chat_tail=${JSON.stringify((session?.chat || []).slice(-8))}`);
};
// Item 2gk (v1.2.3): the chat is the surface. The only thing that can be over
// it is Settings, so getting back to it means closing that.
const ensureChat = async () => {
  if (await page.locator("#chat-task").isVisible()) return;
  const sheet = page.locator("#settings-page");
  if (await sheet.isVisible()) await page.locator(".shell-settings").click();
  await page.locator("#chat-task").waitFor({ state: "visible", timeout: 15000 });
};
// Item 2ge: send and stop are one control, so while a run is live that button
// is Stop and clicking it would cancel rather than queue. Enter is the send
// path the operator uses and the one the item keeps -- "Enter still sends" --
// so the suite sends the way he does.
const setTask = async (text) => {
  await ensureChat();
  await page.locator("#chat-task").fill(text);
  await page.locator("#chat-task").press("Enter");
};
const clickText = async (selector, text) => {
  const exact = new RegExp(`^${text.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`);
  await page.locator(selector).filter({ hasText: exact }).click();
  return true;
};
const clickPendingApproval = async (text, expectedCallID, previousCard = null) => {
  if (previousCard) await page.waitForFunction((element) => !element.isConnected, previousCard);
  const card = page.locator("#chat-pending-approval .approval-card");
  await card.waitFor({ state: "visible" });
  const handle = await card.elementHandle();
  const requestPromise = page.waitForRequest((request) => request.url().endsWith("/api/approve") && request.method() === "POST");
  await card.locator("button").filter({ hasText: new RegExp(`^${text}$`) }).click();
  const body = JSON.parse((await requestPromise).postData() || "{}");
  assert.equal(body.call_id, expectedCallID, `approval card submitted ${body.call_id} instead of ${expectedCallID}`);
  return handle;
};
const state = () => json(`http://127.0.0.1:${appPort}/api/state`);
// Item 2er: the fixtures below replace a live session's client transcript. A
// projection patch for that session arriving afterwards (a run's stop, a budget)
// replaced the fixture and the next wait timed out, more often under load. A
// fixture is injected only once the session has settled: the server's cursor
// for it is unchanged across two reads and the page has applied that cursor.
// Item 2eo: a Steps fold with at most one tool call and one thought has no
// header and is always open; a fold that has a header is opened by clicking it.
async function openStepFoldIfDrawn() {
  const heads = page.locator(".chat-step-summary:visible");
  if (await heads.count()) await heads.first().click();
}

// Item 2gk (v1.2.3): the six groups are two sections of Settings now, so the
// suite opens them the way the operator does - the gear, and then the section.
async function openPanel(section, sessionID) {
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${encodeURIComponent(sessionID)}`);
  await page.locator("#chat-task").waitFor({ state: "visible" });
  if (!(await page.locator("#settings-page").isVisible())) await page.locator(".shell-settings").click();
  await browser.wait(`document.querySelector('#settings-page') && !document.querySelector('#settings-page').hidden`, `Settings ${section}`);
  assert.equal(await clickText(".settings-nav button", section === "agents" ? "Agents" : "Activity"), true);
  await page.locator(`#${section}-panel`).waitFor({ state: "visible" });
}

async function settleSession(id, what) {
  const deadline = Date.now() + 60000;
  let previous = "";
  while (Date.now() < deadline) {
    const server = (await state()).sessions?.[id];
    const cursor = JSON.stringify(server?.cursor || null);
    const idle = !["running", "queued", "paused", "stopping"].includes(server?.run?.status);
    const client = await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); return JSON.stringify(bus.store.sessions[${JSON.stringify(id)}]?.cursor || null); })()`);
    if (idle && cursor === previous && client === cursor) return;
    previous = cursor;
    await sleep(400);
  }
  throw new Error(`session ${id} did not settle before ${what}`);
}
const sessionEvents = async (sessionID) => {
  const files = (await readdir(join(args.data, "logs"))).filter((name) => name.endsWith(".jsonl"));
  const values = [];
  for (const file of files) {
    const lines = (await readFile(join(args.data, "logs", file), "utf8")).split(/\r?\n/).filter(Boolean);
    for (const line of lines) {
      const event = JSON.parse(line);
      if (!sessionID || event.session_id === sessionID) values.push(event);
    }
  }
  return values;
};
const waitEvent = async (sessionID, predicate, label, timeout = 12000) => {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    const found = (await sessionEvents(sessionID)).find(predicate);
    if (found) return found;
    await sleep(50);
  }
  throw new Error(`JSONL timeout: ${label}`);
};

const appPort = await freePort();
const gitPath = spawnSync("where.exe", ["git.exe"], { encoding: "utf8" }).stdout.split(/\r?\n/).find(Boolean);
assert.ok(gitPath, "Git is required for the Run as you acceptance scenario");
const bound = join(args.workspace, "..", "acceptance-bound");
await mkdir(args.workspace, { recursive: true });
await mkdir(bound, { recursive: true });
spawnSync("git.exe", ["init", "--quiet", bound], { stdio: "inherit" });
const attachment = join(args.workspace, "acceptance-attachment.txt");
await writeFile(attachment, "attachment acceptance bytes\n");
await mkdir(join(args.workspace, "reports"), { recursive: true });
await writeFile(join(args.workspace, "reports", "final.txt"), "delivered file\n");
await mkdir(join(args.data, "attachments"), { recursive: true });
await writeFile(join(args.data, "attachments", "phone-note.txt"), "operator attachment bytes\n");
await writeFile(join(bound, "AGENTS.md"), "Use the acceptance rules.\n");
await writeFile(join(bound, "long-tool.txt"), Array.from({ length: 100 }, (_, index) => `tool detail line ${index + 1}`).join("\n"));
if (!realModel) await startFake();
const profileURL = realModel ? args["real-model-url"] : `http://127.0.0.1:${modelPort}`;
const profileName = realModel ? args["real-model-name"] : "agentb-fake";
const appEnvironment = realModel ? process.env : { ...process.env, AGENTB_UPDATE_FIXTURE_URL: `http://127.0.0.1:${modelPort}/agentb-release/latest` };
const toolset = ["read_file", "list_dir", "write_file", "edit_file", "search", "shell", "remember", "recall", "fetch_url", "web_search", "run_script", "call_service"];
const config = {
  config_version: 6, listen: `127.0.0.1:${appPort}`, workspace: args.workspace, log_dir: join(args.data, "logs"),
  servers: [{ id: "acceptance", label: "Acceptance", base_url: profileURL, model: profileName, credential: "", request_timeout_s: 3, probe_mode: "off",
    sampling: { thinking: { temperature: .6, top_p: .95, top_k: 20, min_p: 0, presence_penalty: 0, repeat_penalty: 1 }, nonthinking: { temperature: .7, top_p: .8, top_k: 20, min_p: 0, presence_penalty: 0, repeat_penalty: 1 } },
    reasoning: { control: "auto", enabled: false, effort: "medium", valid_efforts: [], preserve: false }, context: { n_ctx: 32768, reserve_output: 10240 }, system_prompt_override: "",
    capabilities: { server: "agentb-fake", props: true, n_ctx: 32768, tokenize: true, apply_template: true, apply_template_tools: true, streaming: true, tool_calls: true, grammar_constrained: false, cached_tokens: true, timings: false, prompt_progress: false, document_input: false, image_input: false, reasoning_control: "", valid_efforts: [], overflow_behavior: "error", probed_at: new Date().toISOString(), findings: ["acceptance fake"] } }],
  services: {}, agents: [{ name: "Acceptance", b: "acceptance", toolset }], chat: { auto_rename: false },
	updates: { auto_check: !realModel },
  run: { max_turns: 12, cycle_window: 8, max_consecutive_tool_errors: 3, max_concurrent: 2, queue_depth: 0 }, approval: { mode: "boundary-only" },
  // The exchange folder is its own tree, as on an install (item 2fo: inside the
  // workspace it made the hardening check refuse, and Settings logged a 500).
  deliver: { mode: "chips", exchange_folder: join(args.data, "..", "exchange") }, context: { soft_pct: .75, summary_pct: .85, accounting: "auto" }, memory: { enabled: false, dir: join(args.data, "memory"), max_tokens: 1500 },
	operator_files: { allow_mailbox_approvals: false, log_retention_days: 30 },
  tools: { read_file: { default_limit: 16384, max_limit: 65536 }, attachments: { max_bytes: 8388608 }, list_dir: { max_entries: 300, ignore: [".git"] }, grep: { max_matches: 50, max_line_chars: 200 }, shell: { operator_commands: [gitPath] }, fetch: { timeout_s: 20, max_bytes: 2097152, max_redirects: 5, default_limit: 16384, max_limit: 65536, allow_domains: [], deny_domains: [], allow_internal_hosts: [] }, find_files: { skip_roots: [] } },
  shell: { command: ["powershell", "-NoProfile", "-NonInteractive", "-Command"], timeout_s: 60, max_timeout_s: 600, max_output_lines_head: 60, max_output_lines_tail: 40, file_routing_guard: true, operator_context: false, operator_context_idle_timeout_minutes: 20, service_account: { enabled: true, account: "agentb-svc", domain: "." }, deny: [] },
  signing: { thumbprint: "", timestamp_url: "http://timestamp.digicert.com" }
};
await writeFile(join(args.data, "harness.json"), JSON.stringify(config, null, 2));
app = spawn(join(args.app, "Agent_b.exe"), ["-config", join(args.data, "harness.json"), "-app-root", args.app, "-data-root", args.data], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"], env: appEnvironment });
children.push(app);
app.stdout.on("data", (chunk) => process.stdout.write(chunk));
app.stderr.on("data", (chunk) => process.stderr.write(chunk));
await waitHTTP(`http://127.0.0.1:${appPort}/api/state`);
const runtimeState = await state();
if (args["expected-commit"]) assert.equal(runtimeState.build?.commit, args["expected-commit"], "running build commit must match the requested source");
if (args["expected-dirty"]) assert.equal(runtimeState.build?.dirty, args["expected-dirty"] === "true", "running build dirty state must match the requested source");
await mkdir(args.evidence, { recursive: true });
await writeFile(join(args.evidence, "runtime-build.json"), JSON.stringify(runtimeState.build, null, 2));
const loadedConfig = await json(`http://127.0.0.1:${appPort}/api/config`);
assert.equal(loadedConfig.shell?.service_account?.enabled, false, "a configured split without an authenticating credential must turn itself off");
assert.equal(loadedConfig.servers?.[0]?.request_timeout_s, 3, "slow-accounting fixture needs a three-second request timeout");
assert.equal(loadedConfig.servers?.[0]?.capabilities?.tokenize, true, "slow-accounting fixture needs exact tokenization");
assert.equal(loadedConfig.context?.accounting, "auto", "slow-accounting fixture needs automatic exact accounting");
// Item 2er: the Edge profile lives inside the disposable root, so every run
// starts with a new profile and none is left behind in %TEMP%.
edgeContext = await chromium.launchPersistentContext(join(args.data, "..", "..", "edge-profile"), {
  channel: "msedge",
  headless,
  // Headless hides scrollbars by default; keep them so captures still show them.
  ignoreDefaultArgs: ["--hide-scrollbars"],
  viewport: { width: 1250, height: 975 },
  // Item 2ga: software rendering, so an antialiased edge is painted the same
  // way in every run and the screenshot gate compares like with like.
  args: [`--app=http://127.0.0.1:${appPort}/chat`, "--window-size=1250,975", "--disable-gpu"],
});
await edgeContext.addInitScript(() => {
  const timing = window.__agentbLoadTiming = { dom_content_loaded: null, load: null, event_source_constructed: null, event_source_open: null, snapshot: null, first_surface_content: null, state_fetches: [] };
  document.addEventListener("DOMContentLoaded", () => { timing.dom_content_loaded = performance.now(); });
  window.addEventListener("load", () => { timing.load = performance.now(); });
  const NativeEventSource = window.EventSource;
  window.EventSource = class extends NativeEventSource {
    constructor(...values) {
      super(...values);
      timing.event_source_constructed = performance.now();
      this.addEventListener("open", () => { timing.event_source_open ??= performance.now(); });
      this.addEventListener("snapshot", () => { timing.snapshot ??= performance.now(); });
    }
  };
  const nativeFetch = window.fetch.bind(window);
  window.fetch = async (...values) => {
    const path = String(values[0]);
    if (!path.includes("/api/state")) return nativeFetch(...values);
    const sample = { start: performance.now(), end: null };
    timing.state_fetches.push(sample);
    try { return await nativeFetch(...values); }
    finally { sample.end = performance.now(); }
  };
  new MutationObserver(() => {
    if (timing.first_surface_content === null && document.querySelector(".chat-entry,.timeline-run,.timeline-model")) timing.first_surface_content = performance.now();
  }).observe(document, { childList: true, subtree: true });
});
page = edgeContext.pages()[0] || await edgeContext.newPage();
page.on("pageerror", (error) => process.stderr.write(`PAGE ERROR: ${error.stack || error}\n`));
// Item 2fo: console errors, including failed requests, collected for the clean-console scenario.
const consoleErrors = [];
page.on("console", (message) => { if (message.type() === "error") consoleErrors.push({ text: message.text(), url: page.url() }); });
page.on("response", (response) => { if (response.status() >= 400) consoleErrors.push({ text: `HTTP ${response.status()} ${response.request().method()} ${response.url()}`, url: page.url() }); });
browser = {
  evaluate: (expression) => page.evaluate(expression),
  wait: async (expression, label, timeout = 12000) => {
    const deadline = Date.now() + timeout;
    while (Date.now() < deadline) {
      if (await page.evaluate(`Boolean(${expression})`)) return;
      await sleep(50);
    }
    throw new Error(`screen timeout: ${label}`);
  },
};
await page.goto(`http://127.0.0.1:${appPort}/chat`);
await browser.wait(`document.querySelector('#chat-task')`, "Chat opened");
await browser.wait(`document.querySelector('.agent-tab')`, "Agent tab rendered");
if (!realModel) {
	await browser.wait(`document.querySelector('#chat-notice')?.innerText.includes('v9.9.9 available')`, "update line rendered from local fixture");
	record("update-fixture-available-line");
}
record("open-chat");

// Item 2ev: a page stamped by another build reloads itself once and then runs
// the current shell. The first response for the proof URL is a stale copy (the
// current document carrying an earlier build id), standing in for a cached page.
{
  const serverBuild = runtimeState.build?.executable_sha256;
  assert.ok(serverBuild, "server must report its executable hash");
  const documentResponse = await fetch(`http://127.0.0.1:${appPort}/chat`);
  assert.equal(documentResponse.headers.get("cache-control"), "no-store", "the document must never be stored");
  const current = await documentResponse.text();
  assert.ok(current.includes(`<meta name="agentb-build" content="${serverBuild}">`), "the document must carry the serving build");
  const assetResponse = await fetch(`http://127.0.0.1:${appPort}/static/~${serverBuild.slice(0, 12)}/js/build-check.js`);
  assert.equal(assetResponse.headers.get("cache-control"), "no-cache", "assets must be revalidated");
  assert.ok(current.includes(`"/static/~${serverBuild.slice(0, 12)}/js/build-check.js"`), "asset URLs must carry the build id");
  const stale = current.replace(`content="${serverBuild}"`, `content="0000000000000000000000000000000000000000000000000000000000000000"`);
  const proofURL = `http://127.0.0.1:${appPort}/chat?build-proof=1`;
  // The shell may add ?session= before it reloads, so match the proof marker, not the exact URL.
  const isProof = (url) => url.pathname === "/chat" && url.searchParams.get("build-proof") === "1";
  let served = 0;
  await page.route(isProof, async (route) => {
    served += 1;
    if (served === 1) await route.fulfill({ status: 200, contentType: "text/html; charset=utf-8", body: stale });
    else await route.continue();
  });
  await page.goto(proofURL);
  await browser.wait(`document.querySelector('meta[name="agentb-build"]')?.content === ${JSON.stringify(serverBuild)}`, "stale page reloaded to the current build", 15000);
  await browser.wait(`document.querySelector('#chat-task') && document.querySelector('.agent-tab')`, "current shell after the reload");
  await sleep(1500);
  await page.unroute(isProof);
  assert.equal(served, 2, "exactly one automatic reload");
  record("stale-build-page-reloads-once");

  // A window left open across an upgrade: the snapshot its reconnected stream
  // receives names another build, and the page reloads.
  const reloaded = page.waitForEvent("load", { timeout: 15000 });
  await page.evaluate(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const current = await fetch("/api/state", { cache: "no-store" }).then((response) => response.json());
    bus.reduce({ type: "snapshot", data: { ...current, build: { ...current.build, executable_sha256: "f".repeat(64) } } });
  }).catch(() => {});
  await reloaded;
  await browser.wait(`document.querySelector('#chat-task') && document.querySelector('.agent-tab')`, "current shell after the snapshot-driven reload");
  record("open-page-reloads-on-new-build-snapshot");
}

if (realModel) {
  await setTask("Reply with the exact words REAL MODEL ACCEPTANCE OK.");
  await waitProjectedChatText((await state()).active, "REAL MODEL ACCEPTANCE OK", "real model answer", 120000);
  record("real-model-answer");
} else {
  await page.locator(".agent-tab-new").click();
  let snapshot;
  await browser.wait(`new URLSearchParams(location.search).get('session')?.startsWith('s')`, "new session selected");
  record("agent-tab-new-chat-idle");
  // Item 2ew: a genuinely empty chat still says so once the snapshot is in.
  await browser.wait(`[...document.querySelectorAll('.chat-empty')].some((node) => node.innerText.includes('Send a task to start the loop.'))`, "true empty-state text on a new chat");
  snapshot = await state();
  let sessionID = await browser.evaluate(`new URLSearchParams(location.search).get('session')`);
  const session = snapshot.sessions[sessionID];
  assert.equal(session?.id, sessionID, "selected new chat must exist in the server snapshot");
  assert.equal(session?.scratch, true);
  assert.equal(session?.workspace_dir, join(args.data, "scratch", sessionID));
  await browser.wait(`document.querySelector('.shell-session-title')?.innerText === 'Acceptance'`, "profile-name title (item 2eo)");
  assert.equal(await page.locator(".shell-session-title").getAttribute("title"), "Switch model");
  record("new-chat");

  const fixtureSessionID = sessionID;
  await setTask("acceptance: scratch file");
  await waitProjectedChatText(sessionID, "SCRATCH FILE COMPLETE", "scratch file tool");
  assert.equal(await readFile(join(args.data, "scratch", sessionID, "scratch-proof.txt"), "utf8"), "scratch tool passed\n");
  record("scratch-chat-title-and-file-tool");
  sessionID = fixtureSessionID;

  // Item 2ew: a reload while the page is loading, and a late /api/state, never
  // show empty-state text over a chat that has history.
  {
    await edgeContext.addInitScript(() => {
      window.__emptySeen = [];
      new MutationObserver(() => {
        for (const node of document.querySelectorAll(".chat-empty")) {
          const seen = node.innerText.trim();
          if (seen && !window.__emptySeen.includes(seen)) window.__emptySeen.push(seen);
        }
      }).observe(document, { childList: true, subtree: true, characterData: true });
    });
    const historyURL = `http://127.0.0.1:${appPort}/chat?session=${fixtureSessionID}`;
    const historyShown = `document.querySelectorAll('.chat-entry, .chat-response').length > 0`;
    await page.goto(historyURL, { waitUntil: "commit" });
    await page.reload({ waitUntil: "commit" });
    await browser.wait(historyShown, "history after a reload during load");
    assert.deepEqual(await page.evaluate(() => window.__emptySeen), [], "empty-state text was shown during a reload over a chat with history");
    record("reload-shows-no-false-empty-state");
    const lateState = (url) => url.pathname === "/api/state" || url.pathname === "/api/events";
    await page.route(lateState, async (route) => { await sleep(2000); await route.continue().catch(() => {}); });
    await page.goto(historyURL);
    await sleep(1000);
    assert.deepEqual(await page.evaluate(() => window.__emptySeen), [], "empty-state text was shown before a late snapshot");
    await page.unroute(lateState);
    await browser.wait(historyShown, "history after a late snapshot", 15000);
    assert.deepEqual(await page.evaluate(() => window.__emptySeen), [], "empty-state text was shown around a late snapshot");
    record("late-state-shows-blank-then-history");
  }
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await browser.wait(`document.querySelector('#chat-task')`, "fixture chat restored after scratch acceptance");

  await page.locator("#chat-task").fill("acceptance: prose stream");
  await page.locator("#chat-send").click();
  await waitProjectedChatText(sessionID, "VISIBLE PARTIAL", "mid-stream prose partial");
  await browser.wait(`[...document.querySelectorAll('.chat-response-prose')].at(-1)?.innerText.includes('VISIBLE PARTIAL')`, "partial prose visible without expansion");
  const partialProse = await browser.evaluate(`(() => ({
    text: [...document.querySelectorAll('.chat-response-prose')].at(-1)?.innerText || '',
    responseHeaders: document.querySelectorAll('.chat-response-summary').length,
    caret: [...document.querySelectorAll('.chat-response-prose')].at(-1)?.querySelector('.stream-caret')?.isConnected || false,
    status: document.querySelector('#chat-notice')?.innerText || ''
  }))()`);
  assert.match(partialProse.text, /VISIBLE PARTIAL/);
  assert.equal(partialProse.responseHeaders, 0);
  assert.equal(partialProse.caret, true);
  assert.match(partialProse.status, /^writing · \d+ tokens(?: ·|$)/);
  await waitProjectedChatText(sessionID, "VISIBLE PARTIAL COMPLETE", "completed prose stream");
  record("mid-stream-prose-visible-without-expansion");

  assert.equal(await page.locator('.shell-page[aria-label="plan"] .shell-page-chip').count(), 1);
  assert.equal(await page.locator(".shell-page").getAttribute("title"), "plan");
  assert.equal(await page.locator(".shell-settings").count(), 1);
  assert.equal(await page.locator("#chat-title").count(), 0);
  // Item 2gk: a tab had a side and was dressed for it. There is one side now,
  // so what is checked is that the selected tab is still drawn as selected.
  const captureAgentTabStyle = () => page.evaluate(() => {
    const node = document.querySelector('.agent-tab-wrap.selected .agent-tab');
    return { side: node.dataset.side, color: getComputedStyle(node).color, background: getComputedStyle(node.closest(".agent-tab-wrap")).backgroundColor };
  });
  const chatSide = await captureAgentTabStyle();
  assert.equal(chatSide.side, undefined);
  assert.equal(chatSide.color, "rgb(216, 221, 227)");
  const captureShellGeometry = () => page.evaluate(() => Object.fromEntries([
    ["shell", "#app-shell"],
    ["tabs", ".agent-tabs"],
    ["wrap", '.agent-tab-wrap.selected'],
    ["tab", '.agent-tab-wrap.selected .agent-tab'],
    ["plus", ".agent-tab-new"],
    ["plan", ".shell-page"],
    ["settings", ".shell-settings"],
  ].map(([key, selector]) => {
    const rect = document.querySelector(selector).getBoundingClientRect();
    return [key, { x: rect.x, y: rect.y, width: rect.width, height: rect.height }];
  })));
  const captureLoadTiming = () => page.evaluate(() => {
    const navigation = performance.getEntriesByType("navigation")[0];
    return {
      ...window.__agentbLoadTiming,
      ready: performance.now(),
      navigation: navigation ? {
        response_start: navigation.responseStart,
        response_end: navigation.responseEnd,
        dom_interactive: navigation.domInteractive,
        dom_content_loaded: navigation.domContentLoadedEventEnd,
        load: navigation.loadEventEnd,
      } : null,
    };
  });
  const chatGeometry = await captureShellGeometry();
  // Item 2gk: the tab menu no longer has a side to flip to. What has to hold is
  // the route to the numbers and the route back: the gear opens Settings over
  // the chat, and closing it leaves the chat exactly as it was.
  const chatToPanelStarted = performance.now();
  await page.locator(".shell-settings").click();
  await browser.wait(`document.querySelector('#settings-page') && !document.querySelector('#settings-page').hidden`, "Settings open from the chat");
  assert.equal(await clickText(".settings-nav button", "Activity"), true);
  await page.locator("#activity-panel").waitFor({ state: "visible" });
  await page.locator("#panel-lifetime").waitFor({ state: "visible" });
  // Item 2fl: with runs on record, the lifetime numbers are drawn within a
  // second of opening the section, without waiting for an unrelated redraw.
  await browser.wait(`document.querySelector('#panel-stats')?.childElementCount > 0 && !document.querySelector('#panel-stats')?.innerText.includes('No lifetime activity')`, "lifetime numbers within a second", 1000);
  const chatToPanelMS = performance.now() - chatToPanelStarted;
  assert.equal(await page.locator('.shell-page[aria-label="plan"] .shell-page-chip').count(), 1);
  const panelGeometry = await captureShellGeometry();
  assert.deepEqual(panelGeometry, chatGeometry, JSON.stringify({ chatGeometry, panelGeometry }));
	// Agents holds the configurable half. web_search is deliberately file-only
	// in this release, but remains observable as the one additional Activity row.
  assert.equal(await clickText(".settings-nav button", "Agents"), true);
  await page.locator("#agents-panel").waitFor({ state: "visible" });
  const toolHalves = await page.evaluate(() => ({
    toggles: document.querySelectorAll('#panel-tools input[type="checkbox"]').length,
    counts: document.querySelectorAll("#panel-tool-counters .panel-line").length,
    agent: !!document.querySelector("#panel-agent option"),
  }));
	assert.ok(toolHalves.toggles >= 12 && toolHalves.counts === toolHalves.toggles + 1, JSON.stringify(toolHalves));
  assert.equal(toolHalves.agent, true);
  await page.locator('.agent-tab-wrap.selected .agent-tab').click({ button: "right" });
  const toggleMenu = page.locator('.agent-tab-wrap.selected .agent-chat-menu');
  await toggleMenu.waitFor({ state: "visible" });
  assert.equal(await toggleMenu.locator(".agent-chat-console").count(), 0);
  assert.ok(await toggleMenu.locator(".agent-chat-row").count() >= 2);
  assert.equal(await toggleMenu.locator(".agent-chat-close").count(), await toggleMenu.locator(".agent-chat-row").count());
  // Item 2hq: Delete exists on closed rows only. Item 2go: the row remains the
  // date and the name rather than growing another summary line.
  assert.equal(await toggleMenu.locator(".agent-chat-delete").count(), await toggleMenu.locator(".agent-chat-row.closed").count());
  assert.equal(await toggleMenu.locator(".agent-chat-count").count(), 0);
  const historyRow = await toggleMenu.locator(".agent-chat-summary").first().innerText();
  assert.match(historyRow, /^\d{2}:\d{2} · \S/, historyRow);
  // A click outside the shell dismisses an open menu. Escape would dismiss it
  // AND close Settings, which is not what is being measured here.
  await page.locator(".settings-head strong").click();
  await toggleMenu.waitFor({ state: "hidden" });
  const panelToChatStarted = performance.now();
  await page.locator(".shell-settings").click();
  await browser.wait(`document.querySelector('#settings-page')?.hidden`, "Settings closed back onto the chat");
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await page.waitForFunction(() => window.__agentbLoadTiming?.snapshot !== null && document.querySelector(".chat-entry"));
  const panelToChatMS = performance.now() - panelToChatStarted;
  const returnedChatGeometry = await captureShellGeometry();
  const chatLoadTiming = await captureLoadTiming();
  assert.deepEqual(returnedChatGeometry, chatGeometry, JSON.stringify({ chatGeometry, returnedChatGeometry }));
  shellFlipEvidence = { chat: chatGeometry, panels: panelGeometry, returned_chat: returnedChatGeometry, chat_to_panel_ms: chatToPanelMS, panel_to_chat_ms: panelToChatMS, chat_load: chatLoadTiming, tool_halves: toolHalves };
  record("settings-panels-preserve-the-chat-and-the-right-menu");

  // v1.6.2/W0 r3: mountChat asks /api/speech asynchronously. Capturing before
  // that answer is applied races the mic between ordinary and disabled opacity,
  // producing 68 unstable pixels in every Chat surface. A screenshot begins
  // only after the composer carries either the host result or its explicit
  // failure text (both append the middle-dot readiness detail to the title).
  await browser.wait(`document.querySelector('#chat-mic')?.title?.includes(' · ')`, "speech readiness before screenshot capture", 25000);
  const speechReadiness = await page.evaluate(() => {
    const mic = document.querySelector("#chat-mic");
    return { ready: !!mic?.title?.includes(" · "), disabled: !!mic?.disabled, state: mic?.dataset?.state || "", title: mic?.title || "" };
  });
  await writeFile(join(args.evidence, "speech-readiness.json"), `${JSON.stringify(speechReadiness, null, 2)}\n`);

  const shellStateDirectory = join(args.evidence, "shell-states");
  await mkdir(shellStateDirectory, { recursive: true });
  const captureRobotStates = async (pageName, stylesheet) => {
    const boundary = await assertPageStyleBoundary(page, stylesheet);
    const robots = {};
    for (const state of agentStates) {
      robots[state] = await page.evaluate((nextState) => {
        const robot = document.querySelector(".agent-tab-robot");
        if (!robot?.isConnected) throw new Error("agent robot is not attached after tab rerender");
        robot.classList.remove("idle", "waiting", "running", "offline");
        robot.classList.add(nextState);
        const image = robot.querySelector("img");
        const eyes = robot.querySelector(".agent-tab-eyes");
        const robotStyle = getComputedStyle(robot);
        const imageStyle = getComputedStyle(image);
        const eyeStyle = getComputedStyle(eyes);
        const robotRect = robot.getBoundingClientRect();
        const imageRect = image.getBoundingClientRect();
        return {
          robot: { width: robotRect.width, height: robotRect.height, display: robotStyle.display, position: robotStyle.position, color: robotStyle.color },
          image: { width: imageRect.width, height: imageRect.height, display: imageStyle.display, opacity: imageStyle.opacity, transform: imageStyle.transform },
          eyes: { top: eyeStyle.top, width: eyeStyle.width, height: eyeStyle.height, opacity: eyeStyle.opacity, background: eyeStyle.backgroundColor },
        };
      }, state);
      await captureWithMasks(page.locator(".app-shell"), join(shellStateDirectory, `${pageName}-${state}.png`), { specs: [...LIVE_VALUES, OTHER_CHAT_STATE] });
    }
    return { boundary, robots };
  };
  const negativeControl = await provePageStyleBoundaryControl(page, "chat.css");
  const chatStyles = await captureRobotStates("chat", "chat.css");
  await openPanel("activity", sessionID);
  const panelStyles = await captureRobotStates("panels", "app.css");
  const emptyStateIllustration = await page.evaluate(() => {
    const flow = document.querySelector(".flow");
    const fixture = document.createElement("div");
    fixture.className = "idle";
    fixture.innerHTML = '<img src="/static/assets/idle.svg" alt=""><p>fixture</p>';
    flow.append(fixture);
    const fixtureStyle = getComputedStyle(fixture);
    const imageStyle = getComputedStyle(fixture.querySelector("img"));
    const fixtureRect = fixture.getBoundingClientRect();
    const imageRect = fixture.querySelector("img").getBoundingClientRect();
    const result = {
      position: fixtureStyle.position,
      inset: fixtureStyle.inset,
      display: fixtureStyle.display,
      image_width: imageStyle.width,
      image_height: imageStyle.height,
      image_margin: imageStyle.margin,
      container_width: fixtureRect.width,
      image_horizontal_center_delta: (imageRect.left + imageRect.width / 2) - (fixtureRect.left + fixtureRect.width / 2),
    };
    fixture.remove();
    return result;
  });
  assert.deepEqual({
    position: emptyStateIllustration.position,
    inset: emptyStateIllustration.inset,
    display: emptyStateIllustration.display,
    image_width: emptyStateIllustration.image_width,
    image_height: emptyStateIllustration.image_height,
    image_margin: emptyStateIllustration.image_margin,
  }, { position: "absolute", inset: "0px", display: "grid", image_width: "96px", image_height: "96px", image_margin: "0px" });
  assert.ok(emptyStateIllustration.container_width > 96, JSON.stringify(emptyStateIllustration));
  assert.ok(Math.abs(emptyStateIllustration.image_horizontal_center_delta) <= 0.5, JSON.stringify(emptyStateIllustration));
  for (const state of agentStates) assert.deepEqual(panelStyles.robots[state], chatStyles.robots[state], `the robot differs between the sheet and the chat in ${state}`);
  shellStyleBoundaryEvidence = { negative_control: negativeControl, chat: chatStyles, panels: panelStyles, empty_state_illustration: emptyStateIllustration };
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await browser.wait(`document.querySelector('#chat-log')?.innerText.includes('VISIBLE PARTIAL COMPLETE')`, "baseline Chat transcript restored");

  const baselineDirectory = join(args.evidence, "baseline-initial");
  await mkdir(baselineDirectory, { recursive: true });
  const chatIdleScreenshot = await captureWithMasks(page, join(baselineDirectory, "chat-idle.png"));
  await page.locator(".shell-settings").click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
  const profileState = page.locator('.profile-summary[data-id="acceptance"] .profile-state');
  await page.locator('.profile-row:has(.profile-summary[data-id="acceptance"]) [data-action="probe"]').click();
  await browser.wait(`document.querySelector('.profile-summary[data-id="acceptance"] .profile-state')?.textContent.includes('Test passed')`, "Settings Test passed before Chat return");
  assert.match(await profileState.innerText(), /Test passed/);
  // Item 2gf: from Settings, ONE click on the tab reaches the chat. This step
  // used to need the tab menu entry to get back, which is the trap 2gf closed.
  await page.locator('.agent-tab-wrap.selected .agent-tab[data-agent="agent_b"]').click();
  await page.locator("#chat-task").waitFor({ state: "visible" });
  assert.equal(await page.locator("#settings-page").isHidden(), true);
  assert.equal(await page.locator("#settings-page").getAttribute("aria-hidden"), "true");
  assert.deepEqual(await page.screenshot({ animations: "disabled" }), chatIdleScreenshot, "Chat idle changed after Settings → Test → Chat round trip");
  record("settings-test-chat-round-trip");
  await page.locator(".agent-tab").first().click({ button: "right" });
  await page.locator(`.agent-chat-row[data-session="${sessionID}"] .agent-chat-summary`).waitFor({ state: "visible" });
  const initialMenuRows = await page.locator(".agent-chat-row").count();
  // Item 2go: no summary line, and every row is exactly the date and the name.
  assert.equal(await page.locator(".agent-chat-count").count(), 0);
  for (const row of await page.locator(".agent-chat-summary").allInnerTexts()) {
    assert.match(row, /^\d{2}:\d{2} · \S/, row);
    assert.doesNotMatch(row, /run|closed|·.*·/, row);
  }
  await captureWithMasks(page, join(baselineDirectory, "tab-menu-open.png"));
  await openPanel("activity", sessionID);
  await page.locator("#panel-run-result").waitFor({ state: "visible" });
  const runResultText = await page.locator("#panel-run-stop").innerText();
  assert.match(runResultText, /^Ended: done/);
  for (const detector of ["novel_action", "result_repetition", "repeated_timeouts", "model_says_stuck", "error_success_ratio", "baseline_deviation"]) assert.match(runResultText, new RegExp(detector));
  await page.locator('#panel-run-label button[data-label="mixed"]').click();
  await page.locator('#panel-run-label button[data-label="mixed"].selected').waitFor({ state: "visible" });
  assert.equal((await state()).sessions[sessionID].run.result_label, "mixed");
  record("panel-run-result-and-label");
  // Items 2fu and 2fw, on this direct load: the lifetime numbers arrive with no
  // interaction, and History's count heads the rows it draws, one text per line.
  await browser.wait(`document.getElementById('panel-stats')?.innerText.includes('runs / briefs')`, "lifetime numbers on a direct load", 3000);
  const history = await page.evaluate(() => {
    const box = (node) => { const r = node.getBoundingClientRect(); return { x: r.left, y: r.top, w: r.width, h: r.height }; };
    const texts = [...document.querySelectorAll("#timeline-list > .timeline-row > .timeline-head *")].filter((node) => node.childElementCount === 0 && node.textContent.trim() && node.offsetParent).map((node) => ({ text: node.textContent.trim().slice(0, 40), ...box(node) }));
    const overlaps = [];
    for (let i = 0; i < texts.length; i++) for (let j = i + 1; j < texts.length; j++) {
      const a = texts[i], b = texts[j];
      if (a.w && b.w && a.x < b.x + b.w - 1 && b.x < a.x + a.w - 1 && a.y < b.y + b.h - 1 && b.y < a.y + a.h - 1) overlaps.push(`${a.text} × ${b.text}`);
    }
    return { count: document.getElementById("timeline-count").textContent, turns: document.querySelectorAll("#timeline-list > .timeline-model").length, overlaps };
  });
  assert.ok(history.turns > 0, "History drew no turn for a chat that ran");
  assert.match(history.count, new RegExp(`^${history.turns} (of [0-9]+ )?turns`), JSON.stringify(history));
  assert.deepEqual(history.overlaps, [], "History draws one text per line");
  record("panel-direct-load-lifetime-and-history-rows");
  // Item 2gk: the page that was captured here is dissolved. Its two new homes
  // are captured instead, and the chat readout that carries this chat figures.
  await captureWithMasks(page, join(baselineDirectory, "settings-activity.png"));
  assert.equal(await clickText(".settings-nav button", "Agents"), true);
  await page.locator("#agents-panel").waitFor({ state: "visible" });
  await captureWithMasks(page, join(baselineDirectory, "settings-agents.png"));
  assert.equal(await clickText(".settings-nav button", "Connections"), true);
  await browser.wait(`document.querySelector('.settings-content')?.innerText.length > 0`, "Connections drawn");
  await captureWithMasks(page, join(baselineDirectory, "settings.png"));
	if (!realModel) {
		assert.equal(await clickText(".settings-nav button", "About"), true);
		await browser.wait(`document.querySelector('.settings-content')?.innerText.includes('v9.9.9 available')`, "About update action drawn");
		assert.equal(await page.locator('[data-action="install-update"]').innerText(), "Update");
		await captureWithMasks(page, join(baselineDirectory, "settings-about.png"));
		record("settings-about-update-action");
	}
  await page.goto(`http://127.0.0.1:${appPort}/plan?session=${sessionID}`);
  await page.locator('#app-shell[data-page="plan"]').waitFor({ state: "visible" });
  await captureWithMasks(page, join(baselineDirectory, "plan.png"));
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await page.locator("#chat-task").waitFor({ state: "visible" });

  await page.locator("#chat-task").fill("acceptance: menu stream");
  await page.locator("#chat-send").click();
  const lifecycleRunStarted = await waitEvent(sessionID, (event) => event.type === "run.started", "tool-tick lifecycle run started");
  await waitProjectedChatText(sessionID, "menu-stream-0", "first projected lifecycle tool");
  assert.equal(await page.locator(".chat-tool-group-head").count(), 0, "active responses must not regroup live tool nodes");
  const toolButton = page.locator('[data-entry-key*="menu-stream-0"] button.tool-tick');
  await toolButton.waitFor({ state: "visible" });
  assert.equal(await toolButton.evaluate((node) => node.closest('.chat-response')?.querySelector('.chat-step-summary')?.hidden), true, "the active pinned rows have no inert Steps disclosure");
  await toolButton.hover();
  const toolButtonHandle = await toolButton.elementHandle();
  assert.ok(toolButtonHandle, "tool-tick must have an actionable node");
  assert.equal(await toolButtonHandle.evaluate((node) => node.matches(":hover")), true, "tool-tick must be hovered before the event stream advances");
  await toolButtonHandle.evaluate((node) => {
    node.__agentbMutationCount = 0;
    node.__agentbMutationObserver = new MutationObserver((records) => { node.__agentbMutationCount += records.length; });
    node.__agentbMutationObserver.observe(node, { attributes: true, childList: true, characterData: true, subtree: true });
  });
  await page.waitForTimeout(250);
  const toolButtonAfterBeat = await toolButtonHandle.evaluate((node) => ({ attached: node.isConnected, hovered: node.matches(":hover") }));
  assert.equal(toolButtonAfterBeat.attached, true, "tool-tick node changed during the active event stream");
  assert.equal(toolButtonAfterBeat.hovered, true, "tool-tick lost :hover during the active event stream");
  assert.equal(await toolButtonHandle.evaluate((node) => node.__agentbMutationCount), 0, "expanded tool button mutated during the active event stream");
  await toolButtonHandle.click();
  assert.equal(await toolButtonHandle.getAttribute("aria-expanded"), "true", "tool-tick did not expand from a trusted mid-stream click");
  const toolRoot = toolButton.locator("..");
  const collapseArrow = toolRoot.locator("button.collapse-arrow");
  await collapseArrow.waitFor({ state: "visible" });
  await captureWithMasks(page, join(baselineDirectory, "chat-mid-run.png"));
  await toolRoot.evaluate((root) => { root.style.minHeight = "1200px"; });
  await page.evaluate(() => {
    const spacer = document.createElement("div");
    spacer.dataset.acceptanceSpacer = "collapse-arrow";
    spacer.style.height = "1200px";
    document.querySelector("#chat-log")?.append(spacer);
  });
  const arrowBeforeScroll = await collapseArrow.boundingBox();
  assert.ok(arrowBeforeScroll, "collapse arrow must have a visible box after expansion");
  await page.mouse.wheel(0, 600);
  await page.waitForFunction(() => {
    const node = document.querySelector('[data-entry-key*="menu-stream-0"] .collapse-arrow');
    const log = document.querySelector('#chat-log');
    if (!node || !log) return false;
    return Math.abs(node.getBoundingClientRect().top - (log.getBoundingClientRect().top + Number.parseFloat(getComputedStyle(node).top))) < 2;
  });
  const arrowPinned = await collapseArrow.boundingBox();
  // Item 2er: wait for the scroll to land rather than a fixed 50 ms.
  const scrollBeforeTrack = await page.evaluate(() => document.querySelector("#chat-log").scrollTop);
  await page.mouse.wheel(0, 200);
  await page.waitForFunction((before) => document.querySelector("#chat-log").scrollTop > before, scrollBeforeTrack);
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  const arrowDuringScroll = await collapseArrow.boundingBox();
  const arrowTrackingState = await collapseArrow.evaluate((node) => {
    const log = document.querySelector("#chat-log");
    const section = node.parentElement;
    const style = getComputedStyle(node);
    return { scrollTop: log?.scrollTop, log: log?.getBoundingClientRect().toJSON(), section: section?.getBoundingClientRect().toJSON(), position: style.position, top: style.top, float: style.cssFloat };
  });
  assert.ok(arrowPinned && arrowDuringScroll && Math.abs(arrowDuringScroll.y - arrowPinned.y) < 8, `collapse arrow must track while its section remains on screen: ${JSON.stringify({ arrowBeforeScroll, arrowPinned, arrowDuringScroll, arrowTrackingState })}`);
  await page.mouse.wheel(0, 5000);
  await page.waitForFunction(() => {
    const node = document.querySelector('[data-entry-key*="menu-stream-0"] .collapse-arrow');
    const box = node?.getBoundingClientRect();
    return !node || node.hidden || !box || box.bottom < 0 || box.top > innerHeight;
  }, undefined, { timeout: 10000 }).catch(() => {});
  const arrowAfterSection = await collapseArrow.boundingBox();
  // Item 2gk (v1.3.0): this bound used to be the literal 975, which was the
  // viewport height of the day minus the band the readout occupied above the
  // composer. The readout has moved into the status strip, so #chat-log is
  // taller and 975 is no longer the edge of anything. The check is the same
  // check -- the arrow left with its section -- measured instead of guessed,
  // and it is the predicate the waitForFunction above already uses.
  const viewportHeight = await page.evaluate(() => window.innerHeight);
  assert.ok(
    !arrowAfterSection || arrowAfterSection.y + arrowAfterSection.height < 0 || arrowAfterSection.y > viewportHeight,
    `collapse arrow must leave the viewport with its section: ${JSON.stringify({ arrowAfterSection, viewportHeight })}`);
  await page.evaluate(() => document.querySelector('[data-acceptance-spacer="collapse-arrow"]')?.remove());
  await toolRoot.evaluate((root) => { root.style.minHeight = ""; });
  await toolButton.scrollIntoViewIfNeeded();
  await collapseArrow.click();
  await page.waitForFunction((node) => node.getAttribute("aria-expanded") === "false", toolButtonHandle, { timeout: 10000 }).catch(() => {});
  assert.equal(await toolButtonHandle.getAttribute("aria-expanded"), "false", "collapse arrow must collapse its own tool section");
  await collapseArrow.waitFor({ state: "hidden" });
  record("tool-tick-node-lifecycle-active-run");
  // Both failing reads are waited for before the stop, so the transcript this
  // run leaves behind holds two failed calls in every run rather than one or
  // two by timing. Every later capture reads that transcript, so an extra row
  // here moved three of them (v1.2.2/W3).
  await waitProjectedChatText(sessionID, "menu-stream-1", "second projected lifecycle tool");
  await page.locator("#chat-send").click();
  await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.seq > lifecycleRunStarted.seq, "tool-tick lifecycle run stopped");
  await browser.wait(`document.querySelector('#chat-send').dataset.mode === 'send'`, "tool-tick lifecycle stop projected");

  await settleSession(sessionID, "a transcript fixture");

  const missingArgsInitial = await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    session.chat = [
      { type: 'user', key: 'hotfix:user', text: 'before malformed tool' },
      { type: 'tool', key: 'hotfix:missing-args', name: 'read_file' },
      { type: 'agent', key: 'hotfix:answer', text: 'after malformed tool', done: true }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 120));
    const summary = document.querySelector('.chat-step-summary');
    return { collapsed: summary?.innerText || '' };
  })()`);
  await openStepFoldIfDrawn();
  await page.waitForFunction(() => document.querySelectorAll('[data-entry-key^="hotfix:"]').length === 3);
  const missingArgsFixture = await page.evaluate(() => {
    const summary = document.querySelector('.chat-step-summary');
    return {
      rows: document.querySelectorAll('[data-entry-key^="hotfix:"]').length,
      responseAlarm: document.querySelector('.chat-response')?.classList.contains('alarm') || false,
      failureAlarm: document.querySelector('.chat-render-failure')?.classList.contains('alarm') || false,
      text: document.querySelector('#chat-log')?.innerText || ''
    };
  });
  assert.doesNotMatch(missingArgsInitial.collapsed, /failed/);
  assert.equal(missingArgsFixture.rows, 3);
  assert.equal(missingArgsFixture.responseAlarm, false);
  assert.equal(missingArgsFixture.failureAlarm, false);
  assert.match(missingArgsFixture.text, /tool read_file could not render · tool arguments are missing or are not an object/);
  assert.match(missingArgsFixture.text, /before malformed tool/);
  assert.match(missingArgsFixture.text, /after malformed tool/);
  record("missing-tool-args-isolated");

  let events = await sessionEvents(sessionID);
  const beforeRenderFailure = events.at(-1)?.seq || 0;
  await settleSession(sessionID, "a transcript fixture");
  await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    const args = new Proxy({}, { ownKeys() { throw new Error('deliberate render failure'); } });
    session.chat = [
      { type: 'user', key: 'hotfix:kept', text: 'other entry remains' },
      { type: 'tool', key: 'hotfix:throwing', name: 'shell', args }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 65));
    return true;
  })()`);
  await openStepFoldIfDrawn();
  await page.waitForFunction(() => document.querySelector(".chat-render-failure")?.textContent.includes("deliberate render failure"));
  const throwingFixture = await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    for (let index = 1; index < 32; index++) {
      bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
      await new Promise(resolve => setTimeout(resolve, 65));
    }
    const failure = document.querySelector('.chat-render-failure');
    return {
      text: document.querySelector('#chat-log')?.innerText || '',
      responseAlarm: document.querySelector('.chat-response')?.classList.contains('alarm') || false,
      failureAlarm: failure?.classList.contains('alarm') || false
    };
  })()`);
  assert.match(throwingFixture.text, /tool shell could not render · deliberate render failure/);
  assert.match(throwingFixture.text, /other entry remains/);
  assert.doesNotMatch(throwingFixture.text, /No agent connected/);
  assert.equal(throwingFixture.responseAlarm, false);
  assert.equal(throwingFixture.failureAlarm, false);
  await waitEvent(sessionID, (event) => event.type === "ui.error" && event.seq > beforeRenderFailure && event.data?.capped === true, "capped UI render failure", 6000);
  events = await sessionEvents(sessionID);
  const relayedRenderFailures = events.filter((event) => event.type === "ui.error" && event.seq > beforeRenderFailure && event.data?.message?.includes("tool shell deliberate render failure"));
  assert.equal(relayedRenderFailures.length, 2, JSON.stringify(relayedRenderFailures.map((event) => event.data)));
  assert.deepEqual(relayedRenderFailures.map((event) => [event.data.repeat_count, event.data.capped]), [[1, false], [25, true]]);
  record("render-failure-empty-state-and-bounded-relay-2");

  await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); bus.reduce({ type: 'snapshot', data: await fetch('/api/state', { cache: 'no-store' }).then(response => response.json()) }); return true; })()`);
  await browser.wait(`document.querySelector('#chat-log') && !document.querySelector('#chat-log').innerText.includes('deliberate render failure')`, "server snapshot restored");

  await settleSession(sessionID, "a transcript fixture");

  const groupingInitial = await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    session.chat = [
      { type: 'user', key: 'group:user', text: 'group every recorded row' },
      { type: 'tool', key: 'group:read-1', name: 'read_file', args: { path: 'one.txt' }, content: 'ONE COMPLETE', result: { ok: true, ms: 4 } },
      { type: 'agent', key: 'group:thin', reasoning: 'thin recorded thought', reasoningTokens: 12, thinkingMS: 3, done: true },
      { type: 'tool', key: 'group:read-2', name: 'read_file', args: { path: 'two.txt' }, content: 'TWO COMPLETE FAILURE', result: { ok: false, ms: 5 } },
      { type: 'agent', key: 'group:long', reasoning: 'long recorded thought', reasoningTokens: 65, thinkingMS: 7, done: true },
      { type: 'tool', key: 'group:read-3', name: 'read_file', args: { path: 'three.txt' }, content: 'THREE COMPLETE', result: { ok: true, ms: 6 } }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 120));
    const summary = document.querySelector('.chat-step-summary');
    return { collapsed: summary?.innerText || '', collapsedRows: document.querySelectorAll('.chat-step-rows > *').length };
  })()`);
  await openStepFoldIfDrawn();
  const groupingOpen = await page.evaluate(() => {
    const group = document.querySelector('.chat-tool-group-head');
    return { rows: document.querySelectorAll('.chat-step-rows > *').length, groupText: group?.innerText || '' };
  });
  await page.locator(".chat-tool-group-head").click();
  await page.locator('[data-entry-key="group:long"] .thinking-line').click();
  await page.locator('[data-entry-key="group:read-3"] .tool-tick').click();
  const groupingFixture = await page.evaluate(() => ({
      calls: document.querySelectorAll('.chat-tool-group-calls .tool-tick').length,
      details: document.querySelectorAll('.chat-tool-group-calls .tool-detail').length,
      text: document.querySelector('#chat-log')?.innerText || ''
  }));
  assert.match(groupingInitial.collapsed, /3 tool calls · 1 failed · 2 thoughts · 25 ms/);
  assert.equal(groupingInitial.collapsedRows, 0);
  assert.equal(groupingOpen.rows, 3);
  assert.match(groupingOpen.groupText, /read_file ×2 · \+1 thought · 1 failed · 12 ms/);
  assert.equal(groupingFixture.calls, 2);
  assert.equal(groupingFixture.details, 2);
  for (const text of ["ONE COMPLETE", "thin recorded thought", "TWO COMPLETE FAILURE", "long recorded thought", "THREE COMPLETE"]) assert.match(groupingFixture.text, new RegExp(text));
  record("three-level-chat-fold-adjacent-thin-failure-complete");
  await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); bus.reduce({ type: 'snapshot', data: await fetch('/api/state', { cache: 'no-store' }).then(response => response.json()) }); return true; })()`);
  await browser.wait(`document.querySelector('#chat-log') && !document.querySelector('#chat-log').innerText.includes('TWO COMPLETE FAILURE')`, "grouping fixture restored");

  await settleSession(sessionID, "a transcript fixture");

  await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    session.chat = [
      { type: 'user', key: 'arrows:user', text: 'two independent sections' },
      { type: 'tool', key: 'arrows:read', name: 'read_file', args: { path: 'one.txt' }, content: 'one', result: { ok: true } },
      { type: 'tool', key: 'arrows:shell', name: 'shell', args: { command: 'echo two' }, content: 'two', result: { ok: true } }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 120));
    return true;
  })()`);
  await openStepFoldIfDrawn();
  for (const toolTick of await page.locator(".tool-tick").all()) await toolTick.click();
  const twoArrowFixture = await page.evaluate(() => {
    const nodes = [...document.querySelectorAll('button.collapse-arrow')].filter(node => !node.hidden);
    return {
      arrows: nodes.map(node => ({
        arrow: node.getBoundingClientRect().toJSON(),
        section: node.parentElement.getBoundingClientRect().toJSON(),
        position: getComputedStyle(node).position,
        opacity: getComputedStyle(node).opacity
      })),
      horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth
    };
  });
  assert.equal(twoArrowFixture.arrows.length, 2, JSON.stringify(twoArrowFixture));
  for (const band of twoArrowFixture.arrows) {
    assert.equal(band.position, "sticky");
    assert.equal(band.opacity, "0.35");
    assert.ok(band.arrow.top >= band.section.top && band.arrow.bottom <= band.section.bottom, `collapse arrow escaped its section band: ${JSON.stringify(band)}`);
  }
  assert.equal(twoArrowFixture.horizontalOverflow, false);
  await captureWithMasks(page, join(baselineDirectory, "chat-two-expanded-arrows.png"));
  record("two-expanded-sections-two-bounded-arrows");
  await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); bus.reduce({ type: 'snapshot', data: await fetch('/api/state', { cache: 'no-store' }).then(response => response.json()) }); return true; })()`);
  await browser.wait(`document.querySelector('#chat-log') && !document.querySelector('#chat-log').innerText.includes('two independent sections')`, "two-arrow fixture restored");

  await settleSession(sessionID, "a transcript fixture");
  // Item 2er: the delivered file must exist where the chip's probe looks, the
  // session's own folder; it was written only to the legacy workspace, so the
  // probe answered "missing" and the check passed only when it read first.
  const chipFolder = (await state()).sessions[sessionID]?.workspace_dir || args.workspace;
  await mkdir(join(chipFolder, "reports"), { recursive: true });
  await writeFile(join(chipFolder, "reports", "final.txt"), "delivered file\n");
  // Item 2er: the chip renders before its file probe answers and re-renders when
  // it does; read the chip only after that answer, not in between.
  const chipProbe = page.waitForResponse((response) => response.url().includes("/api/files/reports/final.txt") && response.request().method() === "HEAD", { timeout: 30000 }).catch(() => null);
  await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    session.chat = [
      { type: 'user', key: 'chip:user', text: 'show delivered file' },
      { type: 'agent', key: 'chip:agent', run_id: 'chip-run', text: 'DELIVERY READY', toolCallIDs: ['chip-write'], done: true },
      { type: 'tool', key: 'chip:tool', callID: 'chip-write', name: 'write_file', args: { path: 'reports/final.txt' }, result: { ok: true, file: { path: 'reports/final.txt', bytes: 15 } } }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 120));
    return true;
  })()`);
  await openStepFoldIfDrawn();
  await chipProbe;
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  await page.locator('.file-chips .file-chip a').last().waitFor({ state: "visible" });
  const deliveredChip = await page.evaluate(() => {
    const chips = document.querySelectorAll('.file-chips .file-chip');
    const chip = chips[chips.length - 1];
    return {
      text: chip?.innerText || '',
      links: chip?.querySelectorAll('a').length || 0,
      buttons: chip?.querySelectorAll('button').length || 0,
      downloadLinks: chip?.querySelectorAll('a[download]').length || 0,
      linkText: chip?.querySelector('a')?.innerText || '',
      linkTitle: chip?.querySelector('a')?.title || '',
      linkLabel: chip?.querySelector('a')?.getAttribute('aria-label') || '',
      glyph: !!chip?.querySelector('a svg'),
      nameOpenable: !!chip?.querySelector('.file-chip-name.openable'),
      stepsHeaders: chip?.closest('.chat-response')?.querySelectorAll('.chat-step-summary:not([hidden])').length || 0,
      gap: chip ? getComputedStyle(chip).gap : '',
      horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth
    };
  });
  assert.match(deliveredChip.text, /final\.txt/);
  assert.equal(deliveredChip.links, 1);
  assert.equal(deliveredChip.buttons, 0);
  assert.equal(deliveredChip.downloadLinks, 0);
  // Item 2ep: the folder link is a glyph with "folder" on hover; the name opens the document.
  assert.equal(deliveredChip.linkText, "");
  assert.equal(deliveredChip.linkTitle, "folder");
  assert.equal(deliveredChip.linkLabel, "folder");
  assert.equal(deliveredChip.glyph, true);
  assert.equal(deliveredChip.nameOpenable, true);
  assert.equal(deliveredChip.gap, "8px");
  assert.equal(deliveredChip.horizontalOverflow, false);
  await captureWithMasks(page, join(baselineDirectory, "chat-delivered-folder-link.png"));
  assert.equal(deliveredChip.stepsHeaders, 0, "one tool call renders its row without a Steps header (item 2eo)");
  record("delivered-file-chip-folder-link-only");
  await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); bus.reduce({ type: 'snapshot', data: await fetch('/api/state', { cache: 'no-store' }).then(response => response.json()) }); return true; })()`);
  await browser.wait(`document.querySelector('#chat-log') && !document.querySelector('#chat-log').innerText.includes('DELIVERY READY')`, "delivery fixture restored");

  await settleSession(sessionID, "a transcript fixture");

  await browser.evaluate(`(async () => {
    const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href);
    const session = bus.store.sessions[${JSON.stringify(sessionID)}];
    session.run = { ...session.run, status: 'idle' };
    session.chat = [
      { type: 'user', key: 'prose:user', text: 'keep every prose block visible' },
      { type: 'agent', key: 'prose:first', text: 'FIRST PROSE BLOCK', reasoning: 'FIRST PRIVATE THOUGHT', reasoningTokens: 8, done: true },
      { type: 'tool', key: 'prose:first-tool', name: 'read_file', args: { path: 'first.txt' }, content: 'FIRST TOOL RESULT', result: { ok: true, ms: 4 } },
      { type: 'notice', key: 'prose:first-notice', event: { type: 'compaction', data: { before: 20, after: 10 } } },
      { type: 'agent', key: 'prose:second', text: 'SECOND PROSE BLOCK', reasoning: 'SECOND PRIVATE THOUGHT', reasoningTokens: 9, done: true },
      { type: 'tool', key: 'prose:second-tool', name: 'shell', args: { command: 'echo second' }, content: 'SECOND TOOL RESULT', result: { ok: true, ms: 5 } },
      // Item 2eo: two tool calls keep this a group with a header.
      { type: 'tool', key: 'prose:second-tool-2', name: 'search_text', args: { pattern: 'again' }, content: 'AGAIN', result: { ok: true, ms: 3 } }
    ];
    bus.setSelection('agent_b', ${JSON.stringify(sessionID)});
    await new Promise(resolve => setTimeout(resolve, 120));
    return true;
  })()`);
  const firstProseHandle = await page.locator(".chat-response-prose").first().elementHandle();
  assert.ok(firstProseHandle, "first prose block must be actionable");
  const initial = await page.evaluate(() => {
    const response = document.querySelector('.chat-response');
    const folds = [...response.querySelectorAll('.chat-step-summary')];
    const prose = [...response.querySelectorAll('.chat-response-prose')];
    return {
      prose: prose.map(node => node.innerText),
      foldCount: folds.length,
      open: folds.map(node => node.getAttribute('aria-expanded')),
      stepRows: [...response.querySelectorAll('.chat-step-rows')].map(node => node.children.length)
    };
  });
  await page.locator(".chat-step-summary").first().click();
  const afterFirst = await page.evaluate((firstProse) => {
    const response = document.querySelector('.chat-response');
    const folds = [...response.querySelectorAll('.chat-step-summary')];
    return {
      open: folds.map(node => node.getAttribute('aria-expanded')),
      first: folds[0].nextElementSibling.innerText,
      firstKeys: [...folds[0].nextElementSibling.querySelectorAll('[data-entry-key]')].map(node => node.dataset.entryKey),
      secondRows: folds[1].nextElementSibling.children.length,
      proseStable: firstProse === response.querySelectorAll('.chat-response-prose')[0] && firstProse.isConnected
    };
  }, firstProseHandle);
  await page.locator(".chat-step-summary").nth(1).click();
  const afterTurnOpen = await page.evaluate(() => {
    const folds = [...document.querySelectorAll('.chat-response .chat-step-summary')];
    return {
      open: folds.map(node => node.getAttribute('aria-expanded')),
      keys: folds.map(node => [...node.nextElementSibling.querySelectorAll('[data-entry-key]')].map(row => row.dataset.entryKey))
    };
  });
  await page.locator(".chat-step-summary").first().click();
  await page.locator(".chat-step-summary").nth(1).click();
  const final = await page.evaluate((firstProse) => {
    const response = document.querySelector('.chat-response');
    const folds = [...response.querySelectorAll('.chat-step-summary')];
    const prose = [...response.querySelectorAll('.chat-response-prose')];
    return {
      afterTurnClose: folds.map(node => node.getAttribute('aria-expanded')),
      finalProse: prose.map(node => node.innerText),
      proseStable: firstProse === prose[0] && firstProse.isConnected,
      secondCollapsedText: folds[1].nextElementSibling.innerText
    };
  }, firstProseHandle);
  const proseBlocksFixture = { initial, afterFirst, afterTurnOpen: afterTurnOpen.open, afterTurnOpenKeys: afterTurnOpen.keys, ...final };
  assert.deepEqual(proseBlocksFixture.initial.prose, ["FIRST PROSE BLOCK", "SECOND PROSE BLOCK"]);
  assert.equal(proseBlocksFixture.initial.foldCount, 2);
  assert.deepEqual(proseBlocksFixture.initial.open, ["false", "false"]);
  assert.deepEqual(proseBlocksFixture.initial.stepRows, [0, 0]);
  assert.deepEqual(proseBlocksFixture.afterFirst.open, ["true", "false"]);
  assert.equal(proseBlocksFixture.afterFirst.secondRows, 0);
  assert.deepEqual(proseBlocksFixture.afterFirst.firstKeys, ["thought:prose:first", "prose:first-tool", "prose:first-notice"]);
  assert.match(proseBlocksFixture.afterFirst.first, /compacted −10 tokens/);
  assert.equal(proseBlocksFixture.afterFirst.proseStable, true);
  assert.deepEqual(proseBlocksFixture.afterTurnOpen, ["true", "true"]);
  assert.deepEqual(proseBlocksFixture.afterTurnOpenKeys, [
    ["thought:prose:first", "prose:first-tool", "prose:first-notice"],
    ["thought:prose:second", "prose:second-tool", "prose:second-tool-2"]
  ]);
  assert.deepEqual(proseBlocksFixture.afterTurnClose, ["false", "false"]);
  assert.deepEqual(proseBlocksFixture.finalProse, ["FIRST PROSE BLOCK", "SECOND PROSE BLOCK"]);
  assert.equal(proseBlocksFixture.proseStable, true);
  assert.equal(proseBlocksFixture.secondCollapsedText, "");
  await page.setViewportSize({ width: 320, height: 720 });
  const narrowProse = await browser.evaluate(`(() => {
    const response = document.querySelector('.chat-response');
    const prose = response.querySelector('.chat-response-prose').getBoundingClientRect();
    const fold = response.querySelector('.chat-step-fold').getBoundingClientRect();
    const log = document.querySelector('#chat-log');
    return {
      inset: fold.left - prose.left,
      foldRight: fold.right,
      proseRight: prose.right,
      logOverflow: log.scrollWidth - log.clientWidth,
      pageOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth
    };
  })()`);
  assert.ok(narrowProse.inset >= 4 && narrowProse.foldRight <= narrowProse.proseRight + 0.5, JSON.stringify(narrowProse));
  assert.ok(narrowProse.logOverflow <= 0 && narrowProse.pageOverflow <= 0, JSON.stringify(narrowProse));
  await page.setViewportSize({ width: 1250, height: 975 });
  record("prose-always-visible-independent-step-folds-no-horizontal-scroll");
  await browser.evaluate(`(async () => { const bus = await import(new URL("bus.js", document.querySelector("script[src*='/js/build-check.js']").src).href); bus.reduce({ type: 'snapshot', data: await fetch('/api/state', { cache: 'no-store' }).then(response => response.json()) }); return true; })()`);
  await browser.wait(`document.querySelector('#chat-log') && !document.querySelector('#chat-log').innerText.includes('FIRST PROSE BLOCK')`, "prose fixture restored");

  const geometry = await browser.evaluate(`(() => { const textarea=document.querySelector('#chat-task').getBoundingClientRect(); const row=document.querySelector('.chat-composer-row').getBoundingClientRect(); const expand=document.querySelector('#chat-expand').getBoundingClientRect(); const robot=document.querySelector('.agent-tab-wrap.selected .agent-tab-robot').getBoundingClientRect(); const tab=document.querySelector('.agent-tab-wrap.selected').getBoundingClientRect(); const plus=document.querySelector('.shell-left > .agent-tab-new').getBoundingClientRect(); const send=document.querySelector('#chat-send').getBoundingClientRect(); const stop=document.querySelector('#chat-send').getBoundingClientRect(); return {textarea:textarea.width,row:row.width,rowHeight:row.height,expandTop:expand.top-textarea.top,expandRight:textarea.right-expand.right,robot:robot.width,tab:tab.width,plus:{width:plus.width,height:plus.height},send:{width:send.width,height:send.height},stop:{width:stop.width,height:stop.height}}; })()`);
  assert.ok(geometry.textarea >= geometry.row - 50, JSON.stringify(geometry));
  assert.ok(geometry.expandTop >= 0 && geometry.expandTop <= 8 && geometry.expandRight >= 0 && geometry.expandRight <= 8, JSON.stringify(geometry));
  assert.ok(geometry.robot > 0, JSON.stringify(geometry));
  assert.ok(geometry.tab < 180 && geometry.plus.width === 20 && geometry.plus.height === 20, JSON.stringify(geometry));
  assert.deepEqual(geometry.send, geometry.stop, JSON.stringify(geometry));
  assert.ok(Math.abs(geometry.send.height - 24) < 0.01, JSON.stringify(geometry));
  record("composer-flex-width-expand-robot-tab-plus-equal-controls");

  // Item 2ha: the operator asked for the three composer controls to be one
  // family - "i want all 3 icons normalized". Measured, not eyeballed: the hit
  // target, the glyph box and the stroke, at 100% and at 150%.
  const controlFamily = async () => browser.evaluate(`(() => {
    const read = (selector) => {
      const node = document.querySelector(selector);
      if (!node) return null;
      const box = node.getBoundingClientRect();
      const style = getComputedStyle(node);
      const glyph = node.querySelector(".composer-glyph");
      const glyphBox = glyph ? glyph.getBoundingClientRect() : null;
      const glyphStyle = glyph ? getComputedStyle(glyph) : null;
      return {
        target: { width: Math.round(box.width * 100) / 100, height: Math.round(box.height * 100) / 100 },
        background: style.backgroundColor,
        glyph: glyphBox ? { width: Math.round(glyphBox.width * 100) / 100, height: Math.round(glyphBox.height * 100) / 100 } : null,
        stroke: glyphStyle ? glyphStyle.strokeWidth : null,
        fill: glyphStyle ? glyphStyle.fill : null,
      };
    };
    return { attach: read("#chat-attach"), mic: read("#chat-mic"), send: read("#chat-send") };
  })()`);
  const familyAt = {};
  for (const zoom of [1, 1.5]) {
    await page.evaluate((value) => { document.body.style.zoom = value === 1 ? "" : String(value); }, zoom);
    await page.evaluate(() => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done))));
    familyAt[zoom] = await controlFamily();
    const family = familyAt[zoom];
    for (const name of ["attach", "mic", "send"]) assert.ok(family[name], `${name} is missing at ${zoom}: ${JSON.stringify(family)}`);
    assert.deepEqual(family.mic.target, family.attach.target, JSON.stringify({ zoom, family }));
    assert.deepEqual(family.send.target, family.attach.target, JSON.stringify({ zoom, family }));
    assert.deepEqual(family.mic.glyph, family.attach.glyph, JSON.stringify({ zoom, family }));
    assert.deepEqual(family.send.glyph, family.attach.glyph, JSON.stringify({ zoom, family }));
    assert.equal(family.mic.stroke, family.attach.stroke, JSON.stringify({ zoom, family }));
    assert.equal(family.send.stroke, family.attach.stroke, JSON.stringify({ zoom, family }));
    assert.equal(family.mic.background, family.attach.background, JSON.stringify({ zoom, family }));
    assert.equal(family.send.background, family.attach.background, JSON.stringify({ zoom, family }));
    // Line art, so the three are one weight rather than three fonts.
    for (const name of ["attach", "mic", "send"]) assert.equal(family[name].fill, "none", JSON.stringify({ zoom, family }));
  }
  await page.evaluate(() => { document.body.style.zoom = ""; });
  // The stop state is the octagon it always was, and it is the same target.
  const octagon = await browser.evaluate(`(() => {
    const button = document.querySelector("#chat-send");
    button.classList.add("stop-sign");
    if (!button.querySelector(":scope > span[aria-hidden]")) {
      const mark = document.createElement("span");
      mark.setAttribute("aria-hidden", "true");
      button.append(mark);
    }
    const style = getComputedStyle(button);
    const box = button.getBoundingClientRect();
    const glyph = button.querySelector(".composer-glyph");
    const result = {
      clip: style.clipPath,
      target: { width: Math.round(box.width * 100) / 100, height: Math.round(box.height * 100) / 100 },
      glyphHidden: glyph ? getComputedStyle(glyph).display === "none" : null,
      square: !!button.querySelector(":scope > span[aria-hidden]"),
    };
    button.classList.remove("stop-sign");
    button.querySelector(":scope > span[aria-hidden]")?.remove();
    return result;
  })()`);
  assert.match(octagon.clip, /polygon/, JSON.stringify(octagon));
  assert.deepEqual(octagon.target, familyAt[1].attach.target, JSON.stringify({ octagon, family: familyAt[1] }));
  assert.equal(octagon.glyphHidden, true, JSON.stringify(octagon));
  assert.equal(octagon.square, true, JSON.stringify(octagon));
  composerFamilyEvidence = { at100: familyAt[1], at150: familyAt[1.5], octagon };
  record("composer-three-controls-are-one-family");

  events = await sessionEvents(sessionID);
  const beforePlanRegistration = events.at(-1)?.seq || 0;
  await setTask(`Add ${bound} as a plan`);
	const planRegistrationApproval = await waitEvent(sessionID, (event) => event.seq > beforePlanRegistration && event.type === "approval.required" && event.data?.name === "plan registration", "plan registration approval.required");
	await page.reload();
	await browser.wait(`performance.getEntriesByType('navigation')[0]?.type==='reload' && document.querySelector('#chat-task')`, "pending approval refresh");
	await waitFileContains(join(args.data, "OUTBOX.md"), "needs you: approval is waiting");
	record("outbox-line-on-pause");
	await browser.wait(`document.querySelector('.approval-card')`, "plan registration approval");
	assert.equal(await clickText(".approval-card button", "Yes, for this chat"), true);
	await waitEvent(sessionID, (event) => event.seq > planRegistrationApproval.seq && event.type === "run.stopped", "plan registration completed");
	events = await sessionEvents(sessionID);
	const beforeInspectionApproval = events.at(-1)?.seq || 0;
	await setTask(`Please inspect acceptance directory "${bound}" and report.`);
	await waitEvent(sessionID, (event) => event.seq > beforeInspectionApproval && event.type === "approval.required" && event.data?.name === "shell.operator_command", "operator shell approval after unavailable service identity");
	await browser.wait(`document.querySelector('.approval-card')`, "operator shell approval card");
	assert.equal(await clickText(".approval-card button", "Yes, for this chat"), true);
	await waitProjectedChatText(sessionID, "Acceptance answer rendered after the approved shell call.", "answer rendered");
  events = await sessionEvents(sessionID);
  assert.equal(events.some((event) => event.seq > beforeInspectionApproval && event.type === "approval.required" && event.data?.name === "shell.operator_command"), true);
  assert.ok(events.some((event) => event.type === "tool.result" && event.data.name === "shell" && event.data.ok === true), JSON.stringify(events.filter((event) => event.seq > beforeInspectionApproval && (event.type.startsWith("tool.") || event.type.startsWith("approval.") || event.type === "shell.grant")).map((event) => ({ seq: event.seq, type: event.type, data: event.data }))));
  const gutter = await browser.evaluate(`getComputedStyle(document.querySelector('.chat-entry')).gridTemplateColumns.split(' ')[0]`);
  assert.match(gutter, /^72px$/);
  const speakerHeads = await browser.evaluate(`({agent:document.querySelectorAll('.chat-agent .chat-speaker img').length,user:document.querySelectorAll('.chat-user .chat-speaker img').length})`);
  assert.ok(speakerHeads.agent > 0 && speakerHeads.user === 0, JSON.stringify(speakerHeads));
  record("plan-registration-tool-answer-72px-robot-rail");

  const beforeLiveTool = (await sessionEvents(sessionID)).at(-1)?.seq || 0;
  await setTask("acceptance: live tool");
  await waitEvent(sessionID, (event) => event.seq > beforeLiveTool && event.type === "tool.call" && event.data?.name === "shell", "slow live shell call");
  await waitEvent(sessionID, (event) => event.seq > beforeLiveTool && event.type === "stage" && event.data?.stage === "execute" && event.data?.state === "enter", "slow live shell execute stage");
  await sleep(200);
  const liveToolState = await browser.evaluate(`({ status: document.querySelector('#chat-notice')?.innerText || '', carets: document.querySelectorAll('.stream-caret').length, text: document.querySelector('#chat-log')?.innerText || '' })`);
  assert.match(liveToolState.status, /^tool executing · shell(?: ·|$)/);
  assert.equal(liveToolState.carets, 0, JSON.stringify(liveToolState));
  assert.equal(await browser.evaluate(`getComputedStyle(document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')).color`), await browser.evaluate(`(() => { const probe=document.createElement('span'); probe.style.color='var(--trace)'; document.body.append(probe); const value=getComputedStyle(probe).color; probe.remove(); return value; })()`));
  await captureWithMasks(page, join(baselineDirectory, "chat-live-tool.png"));
  await openPanel("activity", sessionID);
  await browser.wait(`document.querySelector('#panel-live-state')?.innerText.startsWith('tool executing · shell')`, "the live run names the slow tool");
  const compactionState = (await state()).sessions[sessionID];
  assert.equal(await page.locator("#panel-live-compactions").innerText(), `${compactionState.compaction_count || 0} compactions · ${compactionState.compaction_model_calls || 0} summaries`);
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth), 0);
  await captureWithMasks(page, join(baselineDirectory, "panel-live-tool.png"));
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await browser.wait(`document.querySelector('#chat-task')`, "chat restored after the live-run proof");
  await waitProjectedChatText(sessionID, "LIVE TOOL COMPLETE", "live-tool final answer");
  record("live-stage-slow-tool-and-stream-caret-lifecycle");

  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
	await browser.wait(`location.pathname==='/chat' && document.querySelector('#settings-page') && document.querySelector('.shell-settings')`, "the settings control on the chat");
  await page.locator(".shell-settings").click();
  await browser.wait(`!document.querySelector('#settings-page').hidden`, "Settings open");
  assert.equal(await clickText(".settings-nav button", "Security"), true);
  await browser.wait(`!document.querySelector('#settings-page').hidden && document.querySelector('.settings-content')?.innerText.includes('Docker Sandbox')`, "operator-file and sandbox Security settings");
  const folderSettings = await browserText(".settings-content");
  for (const text of ["attachments", "Empty", "log retention (days)", "Docker Sandbox"]) assert.ok(folderSettings.includes(text), `Security settings missing ${text}`);
  assert.equal(folderSettings.includes("Adopt repository instructions"), false);
  assert.equal(await page.locator('[data-path="operator_files.log_retention_days"]').inputValue(), "30");
  assert.equal(await browser.evaluate(`document.querySelector('[data-path="operator_files.allow_mailbox_approvals"]')?.getAttribute('aria-checked')`), "false");
  assert.equal(await readFile(join(bound, "AGENTS.md"), "utf8"), "Use the acceptance rules.\n");
  record("scratch-operator-files-and-global-sandbox");
  // Item 2fn: every Settings row, in every section, says what it does on hover.
  const settingsSections = await page.locator(".settings-nav button").allInnerTexts();
  const rowsWithoutHover = [];
  let settingsRows = 0;
  for (const section of settingsSections) {
    assert.equal(await clickText(".settings-nav button", section), true);
    await sleep(150);
    const rows = await browser.evaluate(`[...document.querySelectorAll('.settings-content .setting-row')].map((row) => ({ label: row.querySelector('label')?.textContent.trim() || '', title: (row.getAttribute('title') || '').trim() }))`);
    settingsRows += rows.length;
    for (const row of rows) if (!row.title) rowsWithoutHover.push(`${section}: ${row.label}`);
  }
  assert.ok(settingsRows >= 30, `Settings rows enumerated: ${settingsRows}`);
  assert.deepEqual(rowsWithoutHover, [], "every Settings row carries hover text");
  record("settings-every-row-has-hover-text");
  assert.equal(await clickText(".settings-nav button", "Security"), true);
  await browser.wait(`document.querySelector('.settings-operator-status[data-action="operator-context"]')`, "Settings operator toggle");
  const operatorBefore = await browser.evaluate(`document.querySelector('.settings-operator-status').getAttribute('aria-pressed')`);
  await page.locator(".settings-operator-status").click();
  await browser.wait(`document.querySelector('.settings-operator-status').getAttribute('aria-pressed')!==${JSON.stringify(operatorBefore)}`, "Settings operator toggled");
  await page.locator(".settings-operator-status").click();
  await browser.wait(`document.querySelector('.settings-operator-status').getAttribute('aria-pressed')===${JSON.stringify(operatorBefore)}`, "Settings operator restored");
  record("settings-operator-mode-live-toggle");
  // Item 2fo: the Plan page for a b-chat with no plan, and the ordinary pages,
  // log no console error — no 409 for "no plan", no 404 for /favicon.ico.
  consoleErrors.length = 0;
  await page.goto(`http://127.0.0.1:${appPort}/plan?session=${sessionID}`);
  await browser.wait(`document.querySelector('#plan-list') && (document.querySelector('.plan-entry') || !document.querySelector('#plan-list-empty').hidden)`, "the Plan page drew its list");
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await browser.wait(`document.querySelector('#chat-task')`, "chat for the clean-surface check");
  await openPanel("activity", sessionID);
  await sleep(500);
  assert.deepEqual(consoleErrors, [], "ordinary pages log no console errors");
  const favicon = await fetch(`http://127.0.0.1:${appPort}/favicon.ico`);
  assert.equal(favicon.status, 200, "/favicon.ico serves the icon");
  record("clean-panel-on-ordinary-pages");
	await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
	await browser.wait(`document.querySelector('#chat-task')`, "chat restored after settings");
	events = await sessionEvents(sessionID);
	const beforeUIError = events.at(-1)?.seq || 0;
	await browser.evaluate(`(() => { console.error('acceptance UI relay'); return true; })()`);
	await waitEvent(sessionID, (event) => event.type === "ui.error" && event.seq > beforeUIError && event.data?.kind === "console.error" && event.data?.message?.includes("acceptance UI relay") && event.data?.location?.includes(`/chat?session=${sessionID}`), "UI error relay: console.error");
	await browser.evaluate(`(() => { setTimeout(() => { throw new Error('acceptance unhandled exception'); }, 0); return true; })()`);
	await waitEvent(sessionID, (event) => event.type === "ui.error" && event.seq > beforeUIError && event.data?.kind === "unhandled exception" && event.data?.message?.includes("acceptance unhandled exception") && event.data?.location?.includes(`/chat?session=${sessionID}`), "UI error relay: unhandled exception");
	await browser.evaluate(`(() => { Promise.reject(new Error('acceptance unhandled rejection')); return true; })()`);
	await waitEvent(sessionID, (event) => event.type === "ui.error" && event.seq > beforeUIError && event.data?.kind === "unhandled rejection" && event.data?.message?.includes("acceptance unhandled rejection") && event.data?.location?.includes(`/chat?session=${sessionID}`), "UI error relay: unhandled rejection");
	record("ui-error-relay-three-sources-session-location-jsonl");

  await setTask("acceptance: stop");
  await browser.wait(`document.querySelector('#chat-send').dataset.mode === 'stop'`, "stop enabled");
  await browser.wait(`/(?:prompt|thinking|writing|calling) .*(?:tokens|B|kB|MB)/.test(document.querySelector('#chat-notice .chat-notice-text')?.innerText || '')`, "stop request phase and number");
  const liveStop = await browser.evaluate(`(() => {
    const button = document.querySelector('#chat-send');
    const box = button.getBoundingClientRect();
    const style = getComputedStyle(button);
    return {
      status: document.querySelector('#chat-notice .chat-notice-text')?.innerText || '',
      mode: button.dataset.mode,
      background: style.backgroundColor,
      visible: style.display !== 'none' && style.visibility !== 'hidden' && Number(style.opacity) > 0,
      withinViewport: box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight,
    };
  })()`);
  assert.equal(liveStop.mode, "stop", JSON.stringify(liveStop));
  assert.equal(liveStop.visible, true, JSON.stringify(liveStop));
  assert.equal(liveStop.withinViewport, true, JSON.stringify(liveStop));
  assert.notEqual(liveStop.background, "rgba(0, 0, 0, 0)", JSON.stringify(liveStop));
	const stopPixels = decodePNG(await page.locator("#chat-send").screenshot({ animations: "disabled" }));
	let alarmPixels = 0;
	for (let offset = 0; offset < stopPixels.data.length; offset += 4) {
		if (stopPixels.data[offset] === 0xE4 && stopPixels.data[offset + 1] === 0x62 && stopPixels.data[offset + 2] === 0x4F && stopPixels.data[offset + 3] === 0xFF) alarmPixels++;
	}
	assert.ok(alarmPixels > 200, `live Stop button is not painted alarm red: ${alarmPixels} exact pixels`);
  assert.match(liveStop.status, /(?:prompt|thinking|writing|calling) .*(?:tokens|B|kB|MB)/, JSON.stringify(liveStop));
  const stopStart = Date.now();
  await page.locator("#chat-send").click();
  await browser.wait(`document.querySelector('#chat-send').dataset.mode === 'send'`, "stop completed", 1000);
  assert.ok(Date.now() - stopStart < 1000, `Stop took ${Date.now() - stopStart} ms`);
  await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.data.reason === "aborted_mid_model", "stopped run");
  record("stop-visible-reachable-and-under-one-second");

  // Item 2fg: Stop during a long tool, a message sent while the run is still
  // stopping — the message is held (nothing releases it on a timer or a
  // reachability event), the transcript shows the stop, and only the operator's
  // next message releases the queue, in order.
  events = await sessionEvents(sessionID);
  const beforeStopMidTool = events.at(-1)?.seq || 0;
  await setTask("acceptance: stop mid tool");
  await waitEvent(sessionID, (event) => event.seq > beforeStopMidTool && event.type === "stage" && event.data?.stage === "execute" && event.data?.state === "enter", "60 s tool executing");
  await page.locator("#chat-send").click();
  const sentWhileStopping = await page.evaluate(async (sessionID) => {
    const token = (await (await fetch("/api/state")).json()).mutation_token;
    const response = await fetch("/api/message", { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": token }, body: JSON.stringify({ session_id: sessionID, text: "acceptance: sent while stopping" }) });
    return response.status;
  }, sessionID);
  assert.equal(sentWhileStopping, 202);
  const stoppedMidTool = await waitEvent(sessionID, (event) => event.seq > beforeStopMidTool && event.type === "run.stopped" && event.data?.reason === "aborted_mid_tool", "stopped mid-tool", 10000);
  assert.equal(stoppedMidTool.data.queue_held, true, "a message sent while stopping is held");
  await sleep(3000);
  events = await sessionEvents(sessionID);
  assert.equal(events.some((event) => event.seq > stoppedMidTool.seq && (event.type === "run.started" || event.type === "model.unreachable")), false, "the held message ran, or the stop read as an outage");
  await browser.wait(`[...document.querySelectorAll('#chat-log > .chat-notice-row')].some((row) => row.innerText.startsWith('stopped mid-tool'))`, "stopped-mid-tool marker in the transcript");
  await setTask("acceptance: resume after stop");
  await waitProjectedChatText(sessionID, "Resumed after the stop.", "resume after stop");
  events = await sessionEvents(sessionID);
  const afterStopUsers = events.filter((event) => event.seq > stoppedMidTool.seq && event.type === "message.appended" && event.data.message?.role === "user").map((event) => event.data.message.content);
  assert.deepEqual(afterStopUsers, ["acceptance: sent while stopping", "acceptance: resume after stop"]);
  record("stop-mid-tool-holds-a-message-sent-while-stopping");

	events = await sessionEvents(sessionID);
	const beforeInboxStop = events.at(-1)?.seq || 0;
	await setTask("acceptance: inbox stop");
	await waitEvent(sessionID, (event) => event.type === "model.request" && event.seq > beforeInboxStop, "inbox-stop model request");
	await writeFile(join(args.data, "INBOX.md"), "STOP\n");
	await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.seq > beforeInboxStop && event.data.reason === "mailbox_stop", "INBOX STOP", 15000);
	assert.equal(await readFile(join(args.data, "INBOX.md"), "utf8"), "");
	await waitFileContains(join(args.data, "OUTBOX.md"), "stopped: STOP read from INBOX.md");
	assert.ok((await browserText("#chat-log")).includes("acceptance: inbox stop"));
	record("inbox-stop-mid-run");

	events = await sessionEvents(sessionID);
	const beforeQueueLeader = events.at(-1)?.seq || 0;
	await setTask("acceptance: queue leader");
	await waitEvent(sessionID, (event) => event.seq > beforeQueueLeader && event.type === "model.request", "queue leader model request");
	await browser.wait(`document.querySelector('#chat-send').dataset.mode === 'stop'`, "queue leader running");
	await setTask("acceptance: queued follower");
	await waitEvent(sessionID, (event) => event.type === "message.queued" && event.data.position === 1, "message.queued");
	await browser.wait(`document.querySelector('#chat-status-strip')?.innerText.includes('queued (1)')`, "queued count");
  releaseQueue?.();
  await waitProjectedChatText(sessionID, "Queued follower completed.", "queued follower answer");
  events = await sessionEvents(sessionID);
  const queueUsers = events.filter((event) => event.type === "message.appended" && event.data.message?.role === "user").map((event) => event.data.message.content);
  assert.ok(queueUsers.indexOf("acceptance: queue leader") < queueUsers.indexOf("acceptance: queued follower"));
  record("active-run-queue-fifo");

  await page.locator("#chat-attach").click();
  await page.locator("#chat-attach-exchange").click();
  await browser.wait(`[...document.querySelectorAll('#chat-exchange-files button')].some(item=>item.innerText.includes('phone-note.txt'))`, "operator attachment listed");
  assert.equal(await clickText("#chat-exchange-files button", "phone-note.txt · 26 B"), true);
  await browser.wait(`document.querySelector('.chat-pending-file')`, "pending attachment");
  await setTask("acceptance: attachment");
  await waitProjectedChatText(sessionID, "Attachment received and rendered.", "attachment answer");
  await waitEvent(sessionID, (event) => event.type === "message.appended" && event.data.message?.attachments?.length === 1, "attachment JSONL");
  record("operator-attachments-paperclip-source");
  record("attachment-screen-jsonl");
  const svg = Buffer.from('<svg xmlns="http://www.w3.org/2000/svg"><text>TEXT SVG ACCEPTANCE</text></svg>');
  await page.locator("#chat-file-picker").setInputFiles({ name: "agent.svg", mimeType: "image/svg+xml", buffer: svg });
  await browser.wait(`[...document.querySelectorAll('.chat-pending-file')].some(item=>item.innerText.includes('agent.svg'))`, "SVG pending attachment");
  assert.equal(await page.locator(".chat-pending-file .chat-attachment-warning").count(), 0);
  const beforeSVG = (await sessionEvents(sessionID)).at(-1)?.seq || 0;
  await setTask("acceptance: attachment SVG");
  await waitProjectedChatText(sessionID, "Attachment received and rendered.", "SVG attachment answer");
  const svgMessage = await waitEvent(sessionID, (event) => event.seq > beforeSVG && event.type === "message.appended" && event.data.message?.attachments?.some((item) => item.path.endsWith("agent.svg")), "SVG attachment retained");
  assert.equal(svgMessage.data.message.attachments.find((item) => item.path.endsWith("agent.svg"))?.kind, "text");
  record("svg-attachment-text-on-text-profile");

  const afterSettingsTest = await state();
  const testedProfile = afterSettingsTest.config.servers.find((profile) => profile.id === "acceptance");
  await json(`http://127.0.0.1:${appPort}/api/config`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": afterSettingsTest.mutation_token },
    body: JSON.stringify({ servers: [{ ...testedProfile, probe_mode: "off", capabilities: {
      ...testedProfile.capabilities, server: "agentb-fake", n_ctx: 32768, tokenize: true,
      apply_template: true, apply_template_tools: true, streaming: true, tool_calls: true,
      document_input: false, image_input: false, vision: "rejected", cached_tokens: true,
      findings: ["acceptance fake"],
    } }] }),
  });
  const ocrPNG = await page.evaluate(async () => {
    const canvas = document.createElement("canvas");
    canvas.width = 900;
    canvas.height = 220;
    const context = canvas.getContext("2d");
    context.fillStyle = "white";
    context.fillRect(0, 0, canvas.width, canvas.height);
    context.fillStyle = "black";
    context.font = "32px Consolas";
    context.fillText("AgentB OCR acceptance", 40, 80);
    context.fillText("ERROR 42 sample stack trace", 40, 140);
    return canvas.toDataURL("image/png").split(",")[1];
  });
  await page.locator("#chat-file-picker").setInputFiles({ name: "ocr-acceptance.png", mimeType: "image/png", buffer: Buffer.from(ocrPNG, "base64") });
  const ocrAttachment = page.locator(".chat-pending-file").filter({ hasText: "ocr-acceptance.png" });
  await ocrAttachment.waitFor({ state: "visible" });
  assert.match(await ocrAttachment.innerText(), /OCR: ocr-acceptance\.png\.txt/);
  assert.doesNotMatch(await ocrAttachment.innerText(), /cannot read images/);
  // Item 2ga: a capture of a finished run waits until the page shows it
  // finished — Stop idle — and two frames have painted, or it races the run.
  await browser.wait(`document.querySelector('#chat-send')?.dataset.state === 'idle'`, "run idle before the OCR capture");
  // v1.1.3/W7 waited for the scroll to stop moving; v1.2.2/W3 pins where it
  // stops, because stillness alone let two runs of one build settle a
  // screenful apart. The race was in the capture, not in the product.
  // Item 2gz (v1.2.4): the whole-page transcript photograph is retired. What
  // it proved is asserted above and below this line; what it ADDED was a
  // picture of a live transcript, whose rows move with how far a run got and
  // with measured token counts - the reflow that withheld v1.2.2. The
  // transcript is photographed from a checked-in journal instead, by
  // scripts/transcript-fixture-captures.mjs, with no masks at all.
  await setTask("acceptance: attachment OCR");
  await waitProjectedChatText(sessionID, "Attachment received and rendered.", "OCR attachment answer");
  const ocrMessage = await waitEvent(sessionID, (event) => event.type === "message.appended" && event.data.message?.attachments?.some((item) => item.path.endsWith("ocr-acceptance.png")), "OCR attachment retained in JSONL");
  assert.equal(ocrMessage.data.message.attachments.length, 1);
  record("trusted-image-input-ocr-sidecar-before-send-and-retained");

  const beforeReload = (await browserText("#chat-log")).slice(0, 120);
  await page.reload();
  await waitProjectedChatText(sessionID, "Attachment received and rendered.", "chat reopen");
  await browser.wait(`document.querySelectorAll('#chat-log .chat-entry').length > 1`, "chat rows restored after reopen");
  assert.equal((await browserText("#chat-log")).slice(0, 120), beforeReload);
  record("chat-reopen-preserves-screen-and-jsonl");

  events = await sessionEvents(sessionID);
  const beforeSlowAccounting = Math.max(0, ...events.map((event) => event.seq || 0));
  await json(`${profileURL}/arm-slow-accounting`, { method: "POST" });
  await setTask(`acceptance: slow accounting ${"payload ".repeat(800)}`);
  const slowMessage = await waitEvent(sessionID, (event) => event.seq > beforeSlowAccounting && event.type === "message.appended" && event.data?.message?.content?.startsWith("acceptance: slow accounting"), "slow-accounting message", 12000);
  const slowRun = await waitEvent(sessionID, (event) => event.seq > slowMessage.seq && event.type === "run.started" && event.data?.user_message_id === slowMessage.data.message.id, "slow-accounting run", 12000);
  const estimatedBudget = await waitEvent(sessionID, (event) => event.run_id === slowRun.run_id && event.type === "budget" && event.data?.estimated === true, "estimated slow-accounting budget", 12000);
  await browser.wait(`document.querySelector('.chat-budget-tip')?.innerText.includes('estimated')`, "estimated occupancy label");
  const slowStop = await waitEvent(sessionID, (event) => event.run_id === slowRun.run_id && event.type === "run.stopped" && event.seq > estimatedBudget.seq, "slow-accounting run stopped", 20000);
  assert.equal(slowStop.data.reason, "done");
  await waitProjectedChatText(sessionID, "Slow accounting recovered with an estimate.", "slow-accounting answer", 20000);
  events = await sessionEvents(sessionID);
  await writeFile(join(args.evidence, "slow-accounting-events.json"), JSON.stringify({ trace: slowAccountingTrace, events: events.filter((event) => event.seq > beforeSlowAccounting).map((event) => ({ seq: event.seq, type: event.type, data: event.data })) }, null, 2));
  assert.ok(events.some((event) => event.run_id === slowRun.run_id && event.type === "model.busy"));
  assert.ok(events.some((event) => event.run_id === slowRun.run_id && event.type === "budget" && event.seq > estimatedBudget.seq && event.data?.estimated === false));
  record("long-run-slow-accounting");

  await stopFake();
  await setTask("acceptance: unreachable");
  await browser.wait(`document.querySelector('#chat-status-strip')?.innerText.includes('model unreachable')`, "unreachable strip");
  await waitEvent(sessionID, (event) => event.type === "model.unreachable", "model.unreachable");
  await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.data?.reason === "model_unreachable", "unreachable run stopped");
  await browser.wait(`[...document.querySelectorAll('#chat-log > .chat-notice-row')].some((row) => row.innerText.includes('model unreachable ·'))`, "flat unreachable notice");
  const unreachableRows = await page.evaluate(() => {
    const rows = [...document.querySelectorAll("#chat-log > .chat-entry")];
    const user = rows.findLastIndex((row) => row.classList.contains("chat-user") && row.innerText.includes("acceptance: unreachable"));
    const after = user < 0 ? [] : rows.slice(user + 1);
    return {
      user,
      responses: after.filter((row) => row.classList.contains("chat-response")).length,
      step_folds: after.reduce((total, row) => total + row.querySelectorAll(".chat-step-fold").length, 0),
      flat_notices: after.filter((row) => row.classList.contains("chat-notice-row") && row.innerText.includes("model unreachable ·")).length,
    };
  });
  assert.ok(unreachableRows.user >= 0, JSON.stringify(unreachableRows));
  assert.equal(unreachableRows.responses, 0, JSON.stringify(unreachableRows));
  assert.equal(unreachableRows.step_folds, 0, JSON.stringify(unreachableRows));
  assert.equal(unreachableRows.flat_notices, 1, JSON.stringify(unreachableRows));
  // Item 2gz (v1.2.4): the whole-page transcript photograph is retired. What
  // it proved is asserted above and below this line; what it ADDED was a
  // picture of a live transcript, whose rows move with how far a run got and
  // with measured token counts - the reflow that withheld v1.2.2. The
  // transcript is photographed from a checked-in journal instead, by
  // scripts/transcript-fixture-captures.mjs, with no masks at all.
  record("model-unreachable-no-empty-fold-groups");
  await browser.wait(`document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')?.classList.contains('offline')`, "offline agent eyes");
  assert.equal(await browser.evaluate(`getComputedStyle(document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')).color`), await browser.evaluate(`(() => { const probe=document.createElement('span'); probe.style.color='var(--alarm)'; document.body.append(probe); const value=getComputedStyle(probe).color; probe.remove(); return value; })()`));
  assert.equal(await page.locator("#chat-task").isEnabled(), true);
  assert.equal(await page.locator("#chat-send").isEnabled(), true);
  const unreachableText = await browserText("#chat-notice");
  await setTask("acceptance: recovered");
  // Item 2eo: while the model is unreachable the strip reads that alone, so the
  // queued message is observed on the tape, and the strip is checked for the rule.
  await waitEvent(sessionID, (event) => event.type === "message.queued" && event.data.position === 1 && event.data.text?.includes?.("acceptance: recovered") !== false, "recovery queued");
  assert.equal(unreachableText.trim(), "model unreachable", "an unreachable model is the whole strip line (item 2eo)");
  assert.equal((await browserText("#chat-notice")).trim(), "model unreachable", "queued behind an unreachable model, the strip still reads model unreachable alone");
  const automaticRecoveryStarted = Date.now();
  await startFake(modelPort);
  await waitProjectedChatText(sessionID, "Recovered after Retry.", "automatic recovery", 20000);
  await waitEvent(sessionID, (event) => event.type === "model.reachable", "automatic model.reachable");
  assert.ok(Date.now() - automaticRecoveryStarted <= 10000, "automatic recovery exceeded the acceptance bound");
  record("model-unreachable-automatic-release");

  await stopFake();
  events = await sessionEvents(sessionID);
  const retryUnreachableAfter = events.at(-1)?.seq || 0;
  await setTask("acceptance: unreachable retry");
  await waitEvent(sessionID, (event) => event.seq > retryUnreachableAfter && event.type === "model.unreachable", "Retry fixture model.unreachable");
  await setTask("acceptance: recovered");
  await startFake(modelPort);
  await page.locator("#chat-retry-model").click();
  const retryReachable = await waitEvent(sessionID, (event) => event.seq > retryUnreachableAfter && event.type === "model.reachable", "Retry model.reachable");
  await waitEvent(sessionID, (event) => event.seq > retryReachable.seq && event.type === "run.stopped" && event.data?.reason === "done", "Retry released run completed", 20000);
  // Item 2ff: the open page follows the recovery without a reload — the
  // released answer is on screen, and the notice and Retry are gone.
  await browser.wait(`[...document.querySelectorAll('#chat-log *')].filter((node) => node.childElementCount === 0 && node.textContent.trim() === 'Recovered after Retry.').length >= 2`, "recovered answer on the open page");
  await browser.wait(`!document.querySelector('#chat-notice')?.innerText.includes('unreachable') && document.querySelector('#chat-retry-model')?.hidden`, "unreachable notice cleared without a reload");
  await browser.wait(`!document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')?.classList.contains('offline')`, "recovered agent eyes");
  await browser.wait(`document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')?.classList.contains('idle')`, "idle recovered eyes");
  assert.equal(await browser.evaluate(`getComputedStyle(document.querySelector('.agent-tab-wrap.selected .agent-tab-robot')).color`), await browser.evaluate(`(() => { const probe=document.createElement('span'); probe.style.color='var(--mute)'; document.body.append(probe); const value=getComputedStyle(probe).color; probe.remove(); return value; })()`));
  // Item 2ga: a capture of a finished run waits until the page shows it
  // finished — Stop idle — and two frames have painted, or it races the run.
  await browser.wait(`document.querySelector('#chat-send')?.dataset.state === 'idle'`, "run idle before the retry capture");
  await page.evaluate(() => new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve))));
  // Item 2gz (v1.2.4): the whole-page transcript photograph is retired. What
  // it proved is asserted above and below this line; what it ADDED was a
  // picture of a live transcript, whose rows move with how far a run got and
  // with measured token counts - the reflow that withheld v1.2.2. The
  // transcript is photographed from a checked-in journal instead, by
  // scripts/transcript-fixture-captures.mjs, with no masks at all.
  record("model-unreachable-retry-release");

  await stopFake();
  events = await sessionEvents(sessionID);
  const testUnreachableAfter = events.at(-1)?.seq || 0;
  await setTask("acceptance: unreachable settings test");
  await waitEvent(sessionID, (event) => event.seq > testUnreachableAfter && event.type === "model.unreachable", "Settings Test fixture model.unreachable");
  await startFake(modelPort);
  await page.locator(".shell-settings").click();
  await page.locator("#settings-page").waitFor({ state: "visible" });
  await page.locator('.profile-row:has(.profile-summary[data-id="acceptance"]) [data-action="probe"]').click();
  await waitEvent(sessionID, (event) => event.seq > testUnreachableAfter && event.type === "model.reachable", "Settings Test model.reachable");
  await page.locator('.agent-tab-wrap.selected .agent-tab[data-agent="agent_b"]').click();
  assert.equal(await page.locator("#settings-page").isHidden(), true);
  assert.equal((await state()).sessions[sessionID].model_unreachable || null, null);
  record("model-unreachable-settings-test-release");

  events = await sessionEvents(sessionID);
  const beforeBusy = events.at(-1)?.seq || 0;
  await setTask("acceptance: busy");
  const busyEvent = await waitEvent(sessionID, (event) => event.seq > beforeBusy && event.type === "model.busy", "model busy event", 6000);
  await page.waitForTimeout(250);
  const busyStatus = await page.locator('#chat-notice .chat-notice-text').innerText();
  assert.match(busyStatus, /^prompt \d+ tokens processing(?: ·|$)/);
  events = await sessionEvents(sessionID);
  assert.equal(events.slice(events.indexOf(busyEvent)).some((event) => event.type === "run.stopped"), false);
  releaseBusy?.();
  await waitProjectedChatText(sessionID, "Busy model resumed.", "busy resumed");
  await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.seq > busyEvent.seq, "busy run stopped");
  record("model-busy-waits-without-stop");

  const beforeCompactionSequence = (await sessionEvents(sessionID)).at(-1)?.seq || 0;
  for (let index = 0; index < 12; index++) {
    events = await sessionEvents(sessionID);
    const beforeSequence = events.at(-1)?.seq || 0;
    await setTask(`acceptance: compaction ${index} ${"payload ".repeat(1200)}`);
    await waitEvent(sessionID, (event) => event.type === "run.stopped" && event.seq > beforeSequence, `compaction run ${index}`, 20000);
  }
  const compaction = await waitEvent(sessionID, (event) => event.type === "compaction", "compaction", 20000);
  assert.ok(compaction.data.before > compaction.data.after, `compaction must free tokens: ${JSON.stringify(compaction.data)}`);
  events = await sessionEvents(sessionID);
  const requests = events.filter((event) => event.type === "model.request" && event.body && event.seq > beforeCompactionSequence);
  const prefix = (event) => JSON.stringify({ system: event.body.messages?.[0], tools: event.body.tools });
  assert.equal(prefix(requests[0]), prefix(requests.at(-1)));
  const summaryEvent = events.findLast((event) => event.type === "message.appended" && event.data?.message?.category === "summary");
  assert.ok(summaryEvent, "compaction did not append a summary message");
  assert.equal(summaryEvent.data.message.role, "assistant");
  const requestAfterSummary = requests.find((event) => event.seq > summaryEvent.seq);
  assert.ok(requestAfterSummary, "compaction summary was not followed by a model request");
  assert.ok(requestAfterSummary.body.messages.some((message) => message.role === "assistant" && message.content === summaryEvent.data.message.content), "model request did not retain the complete assistant summary");
  assert.ok((await browserText("#chat-log")).includes("acceptance: compaction"));
  const summaryRow = page.locator(".chat-summary").last();
  await summaryRow.waitFor({ state: "visible" });
  assert.equal((await summaryRow.locator(".chat-speaker").innerText()).trim(), "summary");
  const presentedSummary = await summaryRow.innerText();
  assert.doesNotMatch(presentedSummary, /Progress note \(auto-summary of earlier turns\):|\[BEGIN COMPACTION EVIDENCE\]/);
  await summaryRow.scrollIntoViewIfNeeded();
  await captureWithMasks(page, join(args.evidence, "compaction-summary.png"));
  record("compaction-keeps-model-prefix-stable");
	const compactedSession = (await state()).sessions[sessionID];
	assert.ok(compactedSession.compaction_count > 0, JSON.stringify(compactedSession));
	await openPanel("activity", sessionID);
	await browser.wait(`document.querySelector('#panel-live-compactions')?.innerText.includes('compactions')`, "compacted chat figures");
	const compactedFigures = `${compactedSession.compaction_count} compactions · ${compactedSession.compaction_model_calls || 0} summaries`;
	assert.equal(await page.locator("#panel-live-compactions").innerText(), compactedFigures);
	await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
	await browser.wait(`document.querySelector('#chat-task')`, "compacted chat restored after its figures");

  await page.locator(".agent-tab-new").click();
  await browser.wait(`new URLSearchParams(location.search).get('session') && new URLSearchParams(location.search).get('session') !== ${JSON.stringify(sessionID)}`, "isolated grant chat selected");
  const scriptSessionID = await browser.evaluate(`new URLSearchParams(location.search).get('session')`);
	const scriptSessionBeforeRun = (await state()).sessions[scriptSessionID];
	await openPanel("activity", scriptSessionID);
	await browser.wait(`document.querySelector('#panel-live-compactions')?.innerText.includes('compactions')`, "a different chat figures");
	const scriptFigures = `${scriptSessionBeforeRun.compaction_count || 0} compactions · ${scriptSessionBeforeRun.compaction_model_calls || 0} summaries`;
	assert.equal(await page.locator("#panel-live-compactions").innerText(), scriptFigures);
	assert.notEqual(scriptFigures, compactedFigures);
	await page.goto(`http://127.0.0.1:${appPort}/chat?session=${scriptSessionID}`);
	await browser.wait(`document.querySelector('#chat-task')`, "grant chat restored after its figures");
  events = await sessionEvents(scriptSessionID);
  const beforeRunScriptGrant = events.at(-1)?.seq || 0;
  await setTask("acceptance: run-script grant");
  const scriptApproval = await waitEvent(scriptSessionID, (event) => event.seq > beforeRunScriptGrant && event.type === "approval.required" && event.data?.name === "run_script", "first run_script approval");
  const scriptApprovalCard = await clickPendingApproval("Yes, for this chat", scriptApproval.data.call_id);
  const firstScriptOverride = await waitEvent(scriptSessionID, (event) => event.seq > scriptApproval.seq && event.type === "approval.required" && event.data?.name === "run_script.operator_override", "first run_script identity override");
  const firstOverrideCard = await clickPendingApproval("Just once", firstScriptOverride.data.call_id, scriptApprovalCard);
  const secondScriptOverride = await waitEvent(scriptSessionID, (event) => event.seq > firstScriptOverride.seq && event.type === "approval.required" && event.data?.name === "run_script.operator_override", "second run_script identity override");
  await clickPendingApproval("Just once", secondScriptOverride.data.call_id, firstOverrideCard);
  await waitProjectedChatText(scriptSessionID, "RUN SCRIPT SESSION GRANT COMPLETE", "two run_script calls under one chat grant");
  await waitEvent(scriptSessionID, (event) => event.seq > secondScriptOverride.seq && event.type === "run.stopped" && event.data?.reason === "done", "run_script grant scenario stopped");
  events = await sessionEvents(scriptSessionID);
  const scriptApprovals = events.filter((event) => event.seq > beforeRunScriptGrant && event.type === "approval.required" && event.data?.name === "run_script");
  const scriptResults = events.filter((event) => event.seq > beforeRunScriptGrant && event.type === "tool.result" && event.data?.name === "run_script");
  assert.equal(scriptApprovals.length, 1, JSON.stringify(scriptApprovals.map((event) => event.data)));
  assert.equal(scriptResults.length, 2, JSON.stringify(scriptResults.map((event) => event.data)));
  const grantTurn = page.locator(".chat-response").last();
	const grantTurnSummary = grantTurn.locator(".chat-step-summary");
	if (await grantTurnSummary.getAttribute("aria-expanded") !== "true") await grantTurnSummary.click();
	const decidedApproval = page.locator(".approval-decided").filter({ hasText: /allowed for this chat/ }).last();
	await decidedApproval.waitFor({ state: "attached" });
	await decidedApproval.evaluate((decided) => {
		const summary = decided.closest(".chat-response-block")?.querySelector(".chat-step-summary");
		if (summary?.getAttribute("aria-expanded") !== "true") summary?.click();
	});
	await decidedApproval.waitFor({ state: "visible" });
	const decidedApprovalShape = await decidedApproval.evaluate((decided) => {
		const turn = decided.closest(".chat-response");
		return { text: decided.textContent, height: decided.getBoundingClientRect().height, pending: turn?.querySelectorAll(".approval-card").length || 0 };
	});
	assert.deepEqual(decidedApprovalShape?.text, "Allow this: allowed for this chat");
	assert.equal(decidedApprovalShape?.pending, 0);
	assert.equal(await page.locator("#chat-pending-approval").isHidden(), true);
	assert.ok(decidedApprovalShape.height <= 22);
  record("run-script-one-chat-grant-and-resolved-one-line");
  await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
  await browser.wait(`new URLSearchParams(location.search).get('session') === ${JSON.stringify(sessionID)}`, "main acceptance chat restored after grant scenario");

  const screenshot = await page.screenshot();
	await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
	await browser.wait(`location.pathname==='/chat' && document.querySelector('#settings-page') && document.querySelector('.shell-settings')`, "the settings control before Empty");
	await page.locator(".shell-settings").click();
	await browser.wait(`document.querySelector('#settings-page') && !document.querySelector('#settings-page').hidden`, "Settings open before Empty");
	assert.equal(await clickText(".settings-nav button", "Security"), true);
	await browser.wait(`!document.querySelector('#settings-page').hidden && [...document.querySelectorAll('.settings-content button')].some(item=>item.textContent.trim()==='Empty')`, "attachments Empty action");
	assert.equal(await clickText(".settings-content button", "Empty"), true);
	assert.equal(await clickText(".settings-content button", "Confirm empty"), true);
	for (let attempt = 0; attempt < 100; attempt++) {
		if ((await readdir(join(args.data, "attachments"))).length === 0) break;
		await sleep(50);
	}
	assert.deepEqual(await readdir(join(args.data, "attachments")), []);
	record("settings-confirmed-empty-attachments");
	await page.goto(`http://127.0.0.1:${appPort}/chat?session=${sessionID}`);
	await browser.wait(`document.querySelector('#chat-task')`, "chat restored after empty attachments");
	const finalState = await state();
	await json(`http://127.0.0.1:${appPort}/api/sessions/${sessionID}/close`, { method: "POST", headers: { "X-AgentB-Mutation-Token": finalState.mutation_token } });
	assert.equal((await state()).sessions[sessionID]?.closed, true, "close must retain a closed session");
	await json(`http://127.0.0.1:${appPort}/api/sessions/${sessionID}`, { method: "DELETE", headers: { "X-AgentB-Mutation-Token": finalState.mutation_token } });
	// Item 2hq (v1.6.2): close retains the journal; intentional delete exports
	// it first. The export itself is one of
	// the things that outlives the chat, so the proof is the file on disk.
	const exportedPath = await (async () => {
		for (let attempt = 0; attempt < 200; attempt++) {
			const found = [];
			const chats = join(args.data, "chats");
			for (const dir of await readdir(chats).catch(() => [])) {
				for (const name of await readdir(join(chats, dir)).catch(() => [])) if (name.endsWith(".md")) found.push(join(chats, dir, name));
			}
			const newest = found.sort().at(-1);
			if (newest) return newest;
			await sleep(50);
		}
		throw new Error("no exported chat markdown was written");
	})();
	const exportedMarkdown = await readFile(exportedPath, "utf8");
	assert.ok(exportedMarkdown.includes("## Transcript"));
	assert.ok(exportedMarkdown.includes("- tool `shell` · ok"));
	assert.ok(exportedMarkdown.includes("- attachment: `attachments/phone-note.txt`"));
	record("chat-delete-markdown-export");
  const evidenceRun = join(args.evidence, `run-${new Date().toISOString().replaceAll(":", "-")}`);
  await mkdir(evidenceRun, { recursive: true });
  await writeFile(join(evidenceRun, "chat-final.png"), screenshot);
  await page.goto(`http://127.0.0.1:${appPort}/chat`);
  await browser.wait(`document.querySelector('.agent-tab')`, "agent tab after close");
  // Item 2hq: this chat was explicitly closed and then deleted; what it
  // PRODUCED remains even though the retained chat is gone.
  await page.locator(".agent-tab").first().click({ button: "right" });
  await page.locator(".agent-chat-summary").first().waitFor({ state: "visible" });
  assert.equal(await page.locator(`.agent-chat-row[data-session="${sessionID}"]`).count(), 0, "a deleted chat leaves no history entry");
  assert.equal((await state()).sessions[sessionID], undefined, "deleting removed the session registry entry");
  await page.screenshot({ path: join(evidenceRun, "chat-history-after-close.png") });
  // What the chat produced elsewhere: the exported markdown of what was said,
  // the memory it noted, and the plan it registered.
  const anyFileUnder = async (root) => {
    const found = [];
    const walk = async (dir, depth) => {
      if (depth > 3) return;
      for (const name of await readdir(dir).catch(() => [])) {
        const path = join(dir, name);
        if (name.endsWith(".md") || name.endsWith(".jsonl")) found.push(path);
        else await walk(path, depth + 1);
      }
    };
    await walk(root, 0);
    return found;
  };
  const durable = {
    export: exportedPath,
    plans: (await anyFileUnder(join(args.data, "plans"))).length,
    memory: (await anyFileUnder(join(args.data, "memory"))).length,
  };
  // The markdown of what was said, the plans it registered and the memory it
  // noted all outlived the chat.
  assert.ok(durable.export, JSON.stringify(durable));
  assert.ok(durable.plans > 0, JSON.stringify(durable));
  record("delete-removes-the-chat-and-keeps-what-it-produced");
  await page.setViewportSize({ width: 320, height: 975 });
  for (let index = 0; index < 10; index++) {
    const before = await page.locator(".agent-tab-wrap[data-session]").count();
    await page.locator(".shell-left > .agent-tab-new").click();
    await page.waitForFunction((count) => document.querySelectorAll(".agent-tab-wrap[data-session]").length === count + 1, before);
  }
  const tabOverflow = await page.evaluate(() => {
    const strip = document.querySelector(".agent-tabs");
    const widths = [...document.querySelectorAll(".agent-tab-wrap[data-session]")].map((item) => item.getBoundingClientRect().width);
    const plus = document.querySelector(".shell-left > .agent-tab-new").getBoundingClientRect();
    return { count: widths.length, minimum: Math.min(...widths), scroll: strip.scrollWidth - strip.clientWidth, document: document.documentElement.scrollWidth - document.documentElement.clientWidth, plusCount: document.querySelectorAll(".agent-tab-new").length, nestedPlusCount: document.querySelectorAll(".agent-tab-wrap .agent-tab-new").length, plusLeft: plus.left, stripLeft: strip.getBoundingClientRect().left };
  });
  assert.ok(tabOverflow.count >= 10, JSON.stringify(tabOverflow));
  assert.ok(tabOverflow.minimum >= 118, JSON.stringify(tabOverflow));
  assert.ok(tabOverflow.scroll > 0, JSON.stringify(tabOverflow));
  assert.equal(tabOverflow.document, 0, JSON.stringify(tabOverflow));
  assert.equal(tabOverflow.plusCount, 1, JSON.stringify(tabOverflow));
  assert.equal(tabOverflow.nestedPlusCount, 0, JSON.stringify(tabOverflow));
  assert.ok(tabOverflow.plusLeft < tabOverflow.stripLeft, JSON.stringify(tabOverflow));
  // Item 2hq (v1.6.2): tab and row × close without a dialog and retain the
  // chat. A closed row reopens on click. Only its Delete button confirms and
  // permanently removes the retained chat.
  // A chat of its own for this proof, so the scenario does not depend on which
  // of the suite's chats is still open by the time it runs. The strip is back
  // at full width first: the narrow case above scrolls tabs out of reach.
  await page.setViewportSize({ width: 1250, height: 975 });
  const liveNow = await state();
  const created = await json(`http://127.0.0.1:${appPort}/api/sessions`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": liveNow.mutation_token }, body: JSON.stringify({ agent_id: "acceptance" }) });
  const idleCloseID = created.session?.id || created.id;
  assert.ok(idleCloseID, JSON.stringify(created));
  await page.reload();
  await page.locator(`.agent-tab-wrap[data-session="${idleCloseID}"]`).waitFor({ state: "visible" });
  const dialogs = [];
  const collect = async (dialog) => { dialogs.push(dialog.message()); await dialog.dismiss(); };
  page.on("dialog", collect);
  await page.locator(`.agent-tab-wrap[data-session="${idleCloseID}"] .agent-tab-close`).click();
  await page.waitForFunction((id) => !document.querySelector(`.agent-tab-wrap[data-session="${id}"]`), idleCloseID);
  page.off("dialog", collect);
  assert.equal(dialogs.length, 0, "tab close must not open a dialog");
  assert.equal((await state()).sessions[idleCloseID]?.closed, true, "tab close must retain a closed chat");
  await page.locator(".agent-tab").first().click({ button: "right" });
  const closedRow = page.locator(`.agent-chat-row[data-session="${idleCloseID}"]`);
  await closedRow.waitFor({ state: "visible" });
  assert.equal(await closedRow.locator(".agent-chat-delete").count(), 1, "closed row must expose Delete");
	for (const width of [1250, 320]) {
		await page.setViewportSize({ width, height: 975 });
		const menuRows = await page.evaluate(() => [...document.querySelectorAll(".agent-chat-row")].map((row) => {
			const box = row.getBoundingClientRect();
			const children = [...row.children].map((child) => { const value = child.getBoundingClientRect(); return { top: value.top, bottom: value.bottom, text: child.textContent, clipped: child.scrollWidth > child.clientWidth && !child.classList.contains("agent-chat-summary") }; });
			return { height: box.height, top: box.top, bottom: box.bottom, text: row.textContent, children };
		}));
		assert.ok(menuRows.length > 0, `no chat rows at ${width}`);
		for (const row of menuRows) {
			assert.ok(row.height <= 24, JSON.stringify({ width, row }));
			assert.doesNotMatch(row.text, /(?:^|\s)null(?:\s|$)/i, JSON.stringify({ width, row }));
			assert.ok(row.children.every((child) => child.top >= row.top && child.bottom <= row.bottom && !child.clipped), JSON.stringify({ width, row }));
		}
	}
	await page.setViewportSize({ width: 1250, height: 975 });
  await closedRow.locator(".agent-chat-summary").click();
  await page.locator(`.agent-tab-wrap[data-session="${idleCloseID}"]`).waitFor({ state: "visible" });
  assert.equal((await state()).sessions[idleCloseID]?.closed, false, "closed row click must reopen the chat");

  await page.locator(`.agent-tab-wrap[data-session="${idleCloseID}"] .agent-tab`).click({ button: "right" });
  await page.locator(`.agent-chat-row[data-session="${idleCloseID}"] .agent-chat-close`).click();
  await page.waitForFunction((id) => !document.querySelector(`.agent-tab-wrap[data-session="${id}"]`), idleCloseID);
  assert.equal((await state()).sessions[idleCloseID]?.closed, true, "row close must retain a closed chat");
  await page.locator(".agent-tab").first().click({ button: "right" });
  await page.locator(`.agent-chat-row[data-session="${idleCloseID}"] .agent-chat-delete`).waitFor({ state: "visible" });
  const dismiss = async (dialog) => { dialogs.push(dialog.message()); await dialog.dismiss(); };
  page.on("dialog", dismiss);
  await page.locator(`.agent-chat-row[data-session="${idleCloseID}"] .agent-chat-delete`).click();
  await sleep(300);
  page.off("dialog", dismiss);
  assert.equal(dialogs.at(-1), "Delete this chat? Its memory notes, plans and files stay.");
  assert.ok((await state()).sessions[idleCloseID], "a dismissed delete confirm must keep the chat");
  const accept = async (dialog) => { dialogs.push(dialog.message()); await dialog.accept(); };
  page.on("dialog", accept);
  await page.locator(`.agent-chat-row[data-session="${idleCloseID}"] .agent-chat-delete`).click();
  await page.waitForFunction((id) => !document.querySelector(`.agent-chat-row[data-session="${id}"]`), idleCloseID);
  page.off("dialog", accept);
  for (let attempt = 0; attempt < 100 && (await state()).sessions[idleCloseID]; attempt++) await sleep(50);
  assert.equal((await state()).sessions[idleCloseID], undefined, "delete must remove it from the registry");
  assert.equal(await page.locator(`.agent-chat-row[data-session="${idleCloseID}"]`).count(), 0, "a deleted chat leaves no history entry");
  record("close-retains-reopen-restores-delete-confirms-once");
  await page.setViewportSize({ width: 1250, height: 975 });
  record("per-chat-tabs-scroll-without-shrinking-or-page-overflow");
  const retainedStateBeforeRestart = await state();
  const retainedBeforeRestart = Object.keys(retainedStateBeforeRestart.sessions).length;
  const retainedOpenBeforeRestart = Object.values(retainedStateBeforeRestart.sessions).filter((session) => !session.closed).length;
  app.kill();
  await waitForChildExit(app, 5000);
  app = spawn(join(args.app, "Agent_b.exe"), ["-config", join(args.data, "harness.json"), "-app-root", args.app, "-data-root", args.data], { windowsHide: true, stdio: ["ignore", "pipe", "pipe"], env: appEnvironment });
  children.push(app);
  app.stdout.on("data", (chunk) => process.stdout.write(chunk));
  app.stderr.on("data", (chunk) => process.stderr.write(chunk));
  await waitHTTP(`http://127.0.0.1:${appPort}/api/state`);
  await page.reload();
  await browser.wait(`document.querySelectorAll('.agent-tab-wrap[data-session]').length === ${retainedOpenBeforeRestart}`, "retained tabs after application restart");
  const restartedState = await state();
  assert.equal(Object.keys(restartedState.sessions).length, retainedBeforeRestart);
  assert.ok(restartedState.sessions[scriptSessionID]?.messages?.some((message) => message.content?.includes("acceptance: run-script grant")));
  assert.ok((await readdir(join(args.data, "chats"))).filter((name) => name.endsWith(".jsonl")).length >= retainedBeforeRestart);
  // Item 2es: a restored chat keeps its visible transcript, and `+` after the
  // restart is a new, selected, empty chat with nothing of the retained ones.
  assert.ok(restartedState.sessions[scriptSessionID]?.chat?.some((entry) => entry.type === "user" && entry.text?.includes("acceptance: run-script grant")), "a restored chat must keep its transcript");
  const idsBeforePlus = new Set(Object.keys(restartedState.sessions));
  const tabsBeforePlus = await page.locator(".agent-tab-wrap[data-session]").count();
  await page.locator(".shell-left > .agent-tab-new").click();
  await page.waitForFunction((count) => document.querySelectorAll(".agent-tab-wrap[data-session]").length === count + 1, tabsBeforePlus);
  const afterPlus = await state();
  const plusID = Object.keys(afterPlus.sessions).find((id) => !idsBeforePlus.has(id));
  assert.ok(plusID, "+ after a restart must create a new session id");
  await page.waitForFunction((id) => document.querySelector(".agent-tab-wrap.selected")?.dataset.session === id, plusID);
  assert.equal((afterPlus.sessions[plusID].messages || []).length, 0, "+ after a restart must start with no messages");
  assert.equal((afterPlus.sessions[plusID].chat || []).length, 0, "+ after a restart must show an empty transcript");
  assert.notEqual(afterPlus.sessions[plusID].workspace_dir, restartedState.sessions[scriptSessionID].workspace_dir, "+ must get its own scratch folder");
  assert.equal(afterPlus.sessions[plusID].budget?.categories?.files || 0, 0, "+ must carry no files");
  record("chats-transcripts-and-names-survive-application-restart");

  const browserPlanDir = join(args.data, "plans", "browser-plan");
  await mkdir(join(browserPlanDir, "plan", "items"), { recursive: true });
  await writeFile(join(browserPlanDir, "plan.md"), "# Browser plan\n");
  await writeFile(join(browserPlanDir, "NOTES.md"), "");
  await writeFile(join(browserPlanDir, "plan.json"), JSON.stringify({ repo: bound }, null, 2));
  const beforeD = await state();
  await json(`http://127.0.0.1:${appPort}/api/config`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": beforeD.mutation_token },
    body: JSON.stringify({ agents: [{ ...beforeD.config.agents[0], d: "acceptance" }] }),
  });
  await page.reload();
  await browser.wait(`document.querySelector('.shell-left > .agent-tab-new')?.title === 'New chat or plan'`, "d-aware plus");
  await page.locator(".shell-left > .agent-tab-new").click();
  const roleChoices = page.locator(".shell-new-menu .shell-new-choice");
  assert.deepEqual(await roleChoices.allTextContents(), ["agent_b · Acceptance — chat", "agent_d · Acceptance — plan"]);
  await page.screenshot({ path: join(evidenceRun, "d-role-menu.png") });
  await roleChoices.nth(1).click();
  // Item 2go: the tab reads the chat's name - "new chat" until the operator
  // writes one - and the ROLE is on the robot glyph and its hover text.
  await browser.wait(`document.querySelector('.agent-tab-wrap.selected .agent-tab')?.dataset.agent === 'agent_d'`, "unbound d chat identity");
  assert.equal(await page.locator(".agent-tab-wrap.selected .agent-tab-name").innerText(), "new chat");
  assert.equal(await page.locator(".agent-tab-wrap.selected .agent-tab-robot").getAttribute("title"), "agent_d");
  const dState = await state();
  const dSession = Object.values(dState.sessions).find((session) => session.role === "d" && !session.plan_id);
  assert.ok(dSession, JSON.stringify(dState.sessions));
  assert.equal(dSession.server_id, "acceptance");
  assert.equal(dSession.workspace_dir, join(args.data, "scratch", dSession.id));
  // Item 2gl (v1.2.6): the WINDOW title names the chat, because the overlay
  // could not be made to activate and the system strip stays. 2eo's rule is
  // about the header beside the tab strip, which still reads the profile only.
  assert.equal(await page.title(), "Agent_b · new chat");
  assert.equal(await page.locator(".shell-session-title").innerText(), dSession.b_profile || dSession.server_id, "item 2eo: the header reads the profile name only");
  await page.screenshot({ path: join(evidenceRun, "d-plan.png") });
  record("d-plus-unbound-scratch-tab-and-title");
  const boundCreated = await json(`http://127.0.0.1:${appPort}/api/sessions`, {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": dState.mutation_token },
    body: JSON.stringify({ agent_id: "acceptance", role: "d", plan_id: "browser-plan" }),
  });
  const boundD = boundCreated.session;
  // Item 2fc: the Plan page lists plans and shows one raw; the planning chat is
  // its own tab, and its proposals sit in its thread above the composer.
  const planPageURL = `http://127.0.0.1:${appPort}/plan?plan=browser-plan`;
  const chatURL = (id) => `http://127.0.0.1:${appPort}/chat?session=${encodeURIComponent(id)}`;
  await page.goto(planPageURL);
  await browser.wait(`document.querySelector('#plan-raw')?.textContent.includes('# Browser plan')`, "the raw plan on the Plan page");
  assert.ok(await page.locator(".plan-entry.selected").count() === 1, "the selected plan is expanded in the flyout");
  await page.locator("#plan-search").fill("no-such-plan");
  assert.equal(await page.locator(".plan-entry").count(), 0, "search filters the flyout by name");
  await page.locator("#plan-search").fill("");
  assert.ok(await page.locator(".plan-entry").count() >= 1);
  assert.equal(await page.locator(".plan-proposal").count(), 0, "the Plan page has no tray");
  await page.screenshot({ path: join(evidenceRun, "plan-page.png") });
  await page.goto(chatURL(boundD.id));
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await page.locator("#chat-task").fill("acceptance: plan proposals");
  await page.locator("#chat-task").press("Enter");
  await browser.wait(`document.querySelectorAll('#chat-proposals .plan-proposal').length === 3`, "three inert proposals in the planning chat");
  await page.screenshot({ path: join(evidenceRun, "plan-proposals.png") });
  await page.locator('#chat-proposals .plan-proposal').nth(2).click();
  assert.match(await page.locator("#chat-task").inputValue(), /Quoted plan/, "clicking a proposal quotes it");
  await page.locator("#chat-task").fill("");
  await page.locator('#chat-proposals .plan-proposal').nth(1).getByRole('button', { name: 'Dismiss' }).click();
  assert.equal(await page.locator('#chat-proposals .plan-proposal').count(), 2, "Dismiss removes only that proposal");
  await page.locator('#chat-proposals .plan-proposal').nth(0).getByRole('button', { name: 'Accept' }).click();
  await waitFileContains(join(browserPlanDir, "plan.md"), "2t accepted via tray");
  assert.equal(await readFile(join(browserPlanDir, "plan.md"), "utf8"), "# Browser plan\n[ ] 2t accepted via tray\n");
  await page.goto(planPageURL);
  await browser.wait(`document.querySelector('#plan-raw')?.textContent.includes('2t accepted via tray')`, "accepted edit in the raw plan");
  await page.screenshot({ path: join(evidenceRun, "plan-accepted.png") });
  record("plan-empty-propose-dismiss-quote-accept");

  // 2t-ii — Go, the worker's post in the design thread, and the done card.
  // The plan gets two items: one the worker can finish and one it cannot. The
  // worker is its own c-role session, so nothing here is typed into a chat.
  await writeFile(join(browserPlanDir, "plan.md"), "# Browser plan\n\n- [ ] 2t worker reads the repository it was given\n- [ ] 2u worker cannot finish this one\n- [ ] 2v names no verifier\n");
  // v0.63.0: [x] needs a verifier. An item that may finish names one in its item
  // file header; 2v names none, so it is stuck without being started.
  await writeFile(join(browserPlanDir, "plan", "items", "2t.md"), "state: live\nverify: echo verified\n\n# 2t\n");
  await writeFile(join(browserPlanDir, "plan", "items", "2u.md"), "state: live\nverify: echo verified\n\n# 2u\n");
  await writeFile(join(browserPlanDir, "plan", "items", "2v.md"), "state: live\n\n# 2v\n");
  await page.reload();
  await browser.wait(`document.querySelector('#plan-go') && !document.querySelector('#plan-go').disabled`, "Go enabled by a waiting item");
  assert.equal(await page.locator("#plan-go").innerText(), "Go");
  assert.equal(await page.locator("#plan-go").getAttribute("title"), "Run the accepted items in plan order");
  const planHeadHeight = await page.evaluate(() => document.querySelector(".plan-panel-head").getBoundingClientRect().height);
  assert.equal(planHeadHeight, 36, "Go changed the plan header's height");
  await page.screenshot({ path: join(evidenceRun, "plan-go.png") });
  await page.locator("#plan-go").click();
  await browser.wait(`document.querySelector('#plan-go')?.textContent === 'Stop'`, "Go reads Stop while a worker runs");
  await page.screenshot({ path: join(evidenceRun, "plan-worker-running.png") });
  const workerSession = Object.values((await state()).sessions).find((entry) => entry.role === "c");
  assert.ok(workerSession, "the worker did not get its own session");
  assert.equal(workerSession.plan_id, "browser-plan");
  // The worker has no chat: its card is drawn in the planning chat's thread,
  // marked as the worker's, and answered there.
  await page.goto(chatURL(boundD.id));
  await page.locator("#chat-task").waitFor({ state: "visible" });
  assert.equal(await page.locator('.agent-tab-wrap[data-session="' + workerSession.id + '"]').count(), 0, "the worker appeared in the tab strip");
  const planPath = join(browserPlanDir, "plan.md");
  let workerCards = 0;
  for (const deadline = Date.now() + 45000; Date.now() < deadline; ) {
    const card = page.locator("#chat-pending-approval .approval-card.worker-approval");
    if (await card.count()) {
      assert.match(await card.first().innerText(), /agent_c/, "the worker's card does not name the worker");
      if (!workerCards) await page.screenshot({ path: join(evidenceRun, "plan-worker-approval.png") });
      workerCards++;
      await card.first().getByRole("button", { name: "Yes, for this chat" }).click();
      await sleep(250);
      continue;
    }
    if ((await readFile(planPath, "utf8")).includes("[x] [[2t]] 2t")) break;
    await sleep(100);
  }
  if (!workerCards) {
    const serverWorker = Object.values((await state()).sessions).find((entry) => entry.role === "c");
    process.stdout.write(`WORKER CARD DIAGNOSTIC plan=${JSON.stringify(await readFile(planPath, "utf8"))} server_pending=${JSON.stringify(serverWorker?.pending_approval?.event?.data?.name || null)} run=${JSON.stringify(serverWorker?.run?.status || null)}` + String.fromCharCode(10));
  }
  assert.ok(workerCards > 0, "the worker's approval was never drawn in the design thread");
  process.stdout.write(`WORKER APPROVAL IN THREAD answered ${workerCards} card(s)` + String.fromCharCode(10));
  record("worker-approval-card-in-design-thread-answered-there");
  // A worker that did not get where it was going has to say why in the gate's
  // own output, or the next person reads a bare timeout.
  try {
    await waitFileContains(join(browserPlanDir, "plan.md"), "- [x] [[2t]] 2t worker reads the repository it was given", 90000);
  } catch (error) {
    const workers = Object.values((await state()).sessions).filter((entry) => entry.role === "c");
    process.stdout.write(`WORKER DIAGNOSTIC plan=${JSON.stringify(await readFile(join(browserPlanDir, "plan.md"), "utf8"))}\n`);
    for (const entry of workers) process.stdout.write(`WORKER SESSION ${JSON.stringify({ id: entry.id, run: entry.run, runnable: entry.runnable, reason: entry.not_runnable_reason, workspace: entry.workspace_dir, plan: entry.plan_id, messages: (entry.messages || []).map((message) => [message.role, String(message.content || "").slice(0, 160)]) })}\n`);
    for (const event of (await sessionEvents(null)).slice(-40)) process.stdout.write(`WORKER EVENT ${event.type} ${event.session_id} ${JSON.stringify(event.data).slice(0, 220)}\n`);
    throw error;
  }
  await waitFileContains(join(browserPlanDir, "plan.md"), "- [!] [[2u]] 2u worker cannot finish this one  — stuck: tool_errors", 90000);
  await waitFileContains(join(browserPlanDir, "plan.md"), "- [!] [[2v]] 2v names no verifier  — stuck: no verifier named", 90000);
  await page.bringToFront();
  const questionText = "Which database should the cache use?";
  const questionOnScreen = async () => (await browserText("#chat-log")).includes(questionText);
  let postArrival = "live";
  for (const deadline = Date.now() + 40000; !(await questionOnScreen()) && Date.now() < deadline; ) await sleep(250);
  if (!(await questionOnScreen())) {
    postArrival = "after a reload";
    await page.reload();
    await page.waitForSelector("#chat-log");
    for (const deadline = Date.now() + 15000; !(await questionOnScreen()) && Date.now() < deadline; ) await sleep(250);
  }
  assert.ok(await questionOnScreen(), "the worker's question is not in the design thread even after a reload");
  assert.match(await browserText("#chat-log"), /agent_c/, "the question is not attributed to the worker");
  process.stdout.write(`WORKER POST ON SCREEN ${postArrival}` + String.fromCharCode(10));
  const postedEntry = ((await state()).sessions[boundD.id]?.chat || []).find((entry) => entry.event?.type === "c.job" && entry.event?.data?.question === questionText);
  assert.ok(postedEntry, "the worker's question is on screen but not in the planner's projection");
  assert.equal(postedEntry.agent_role, "c", JSON.stringify(postedEntry));
  assert.equal(postedEntry.event.data.routed_to, "d");
  assert.equal(postedEntry.event.data.worker, workerSession.id);
  await page.screenshot({ path: join(evidenceRun, "plan-worker-post.png") });
  const workerJob = await waitEvent(null, (event) => event.type === "c.job" && event.data?.question === questionText, "c.job on the bus");
  assert.equal(workerJob.session_id, boundD.id, "the question was not posted in the planner's thread");
  assert.equal(workerJob.data.routed_to, "d");
  assert.equal(workerJob.data.role, "c");
  assert.equal(workerJob.data.worker, workerSession.id);
  // The item that names no verifier is proposed to the planner: in its thread,
  // with Dismiss and quote, and no Accept, because only the planner can name it.
  await browser.wait(`[...document.querySelectorAll('#chat-proposals .plan-proposal')].some((row) => row.innerText.includes('verifier · plan/items/2v.md · 2v'))`, "the verifier proposal in the planning chat", 20000);
  const verifierRow = page.locator("#chat-proposals .plan-proposal", { hasText: "verifier · plan/items/2v.md · 2v" });
  assert.equal(await verifierRow.getByRole("button", { name: "Accept" }).count(), 0, "a verifier proposal offered Accept");
  assert.equal(await verifierRow.getByRole("button", { name: "Dismiss" }).count(), 1);
  await page.goto(planPageURL);
  await browser.wait(`document.querySelector('#plan-done') && !document.querySelector('#plan-done').hidden`, "the done card", 60000);
  const doneText = await browserText("#plan-done");
  assert.match(doneText, /Ready to test, with gaps/, doneText);
  assert.match(doneText, /1 done · 2 stuck · 0 waiting/, doneText);
  assert.match(doneText, /2u worker cannot finish this one: tool_errors/, doneText);
  assert.match(doneText, /2v names no verifier: no verifier named/, doneText);
  assert.equal(await page.locator("#plan-go").innerText(), "Go");
  assert.equal(await page.locator("#plan-go").isDisabled(), true, "Go stayed live with nothing waiting");
  assert.match(await browserText("#plan-stats"), /1 done · 2 stuck/, "the plan's numbers show the worker's result");
  await page.screenshot({ path: join(evidenceRun, "plan-done-card.png") });
  const markedPlan = await readFile(join(browserPlanDir, "plan.md"), "utf8");
  assert.ok(!markedPlan.includes("[~]"), markedPlan);
  record("plan-go-worker-post-and-done-card");
  record("no-verifier-item-stuck-and-proposed-in-tray");
  // A plan whose repository is inside the plans folder shows why on its panel,
  // and Go is refused rather than starting a worker that could only be refused.
  const planManifest = join(browserPlanDir, "plan.json");
  const originalManifest = await readFile(planManifest, "utf8");
  const insideRepo = join(browserPlanDir, "repo-inside-plans");
  await mkdir(insideRepo, { recursive: true });
  await writeFile(planManifest, JSON.stringify({ repo: insideRepo }, null, 2));
  await writeFile(planPath, markedPlan + "- [ ] 2w would be refused\n");
  await page.reload();
  await browser.wait(`document.querySelector('#plan-refusal')?.innerText.includes('inside the plans folder')`, "the refusal line on the plan panel", 20000);
  await browser.wait(`document.querySelector('#plan-go') && document.querySelector('#plan-go').disabled`, "Go refused for a repo inside the plans folder", 20000);
  await page.screenshot({ path: join(evidenceRun, "plan-refusal.png") });
  await writeFile(planManifest, originalManifest);
  await writeFile(planPath, markedPlan);
  record("repo-inside-plans-refusal-line-and-go-refused");

  // 2gp: a filled three-question brief enters the planning request as scoped
  // data, and the fake planner's accepted plan.md draft carries every field.
  const briefRepo = join(args.data, "..", "brief-repo");
  await mkdir(briefRepo, { recursive: true });
  await page.goto(planPageURL);
  await page.locator("#plan-add").click();
  await page.locator("#plan-add-path").fill(briefRepo);
  await page.locator("#plan-add-path").press("Enter");
  await browser.wait(`document.querySelector('#plan-build') && !document.querySelector('#plan-build').hidden`, "Build plan now for the filled brief");
  const briefPlan = (await (await fetch(`http://127.0.0.1:${appPort}/api/plans`)).json()).find((plan) => plan.repo && plan.repo.toLowerCase().endsWith("brief-repo"));
  assert.ok(briefPlan, "the filled-brief plan was not registered");
  const briefPlanPath = join(args.data, "plans", briefPlan.id, "plan.md");
  planningBriefOriginal = await readFile(briefPlanPath, "utf8");
  await page.locator("#plan-build-yes").click();
  await page.locator("#plan-wizard").waitFor({ state: "visible" });
  await page.screenshot({ path: join(evidenceRun, "planning-1-purpose.png") });
  await page.locator("#plan-wizard-value").fill("Acceptance project purpose");
  await page.locator("#plan-wizard-next").click();
  await page.screenshot({ path: join(evidenceRun, "planning-2-done.png") });
  await page.locator("#plan-wizard-value").fill("Milestone one\nMilestone two\nMilestone three");
  await page.locator("#plan-wizard-next").click();
  await page.screenshot({ path: join(evidenceRun, "planning-3-do-not-touch.png") });
  await page.locator("#plan-wizard-value").fill("Do not touch billing");
  await page.locator("#plan-wizard-next").click();
  await page.waitForURL((url) => url.pathname === "/chat" && !!url.searchParams.get("session"));
  await browser.wait(`document.querySelectorAll('#chat-proposals .plan-proposal').length === 1`, "the filled brief's plan.md draft", 20000);
  const briefProposal = page.locator("#chat-proposals .plan-proposal").first();
  await briefProposal.getByRole("button", { name: "Accept" }).click();
  await waitFileContains(briefPlanPath, "Acceptance project purpose");
  const draftedBriefPlan = await readFile(briefPlanPath, "utf8");
  for (const text of ["Acceptance project purpose", "Milestone one", "Milestone two", "Milestone three", "Do not touch billing"]) assert.match(draftedBriefPlan, new RegExp(text));
  record("planning-wizard-three-fields-carried-into-accepted-plan-draft");

  // Item 2fc: + asks for a folder, registers its plan from the template, and
  // asks "Build plan now?"; Yes opens the planning chat bound to it with the
  // request in its composer — sent by the operator, never by the harness.
  const newRepo = join(args.data, "..", "flyout-repo");
  await mkdir(newRepo, { recursive: true });
  await page.goto(planPageURL);
  await page.locator("#plan-add").click();
  await page.locator("#plan-add-path").fill(join(args.data, "..", "no-such-folder"));
  await page.locator("#plan-add-path").press("Enter");
  await browser.wait(`document.querySelector('#plan-add-error') && !document.querySelector('#plan-add-error').hidden`, "a missing folder is refused with the reason");
  await page.locator("#plan-add-path").fill(newRepo);
  await page.locator("#plan-add-path").press("Enter");
  await browser.wait(`document.querySelector('#plan-build') && !document.querySelector('#plan-build').hidden`, "Build plan now?");
  await browser.wait(`document.querySelector('#plan-raw')?.textContent.includes('## ')`, "the new plan's template on the right");
  const createdPlan = (await (await fetch(`http://127.0.0.1:${appPort}/api/plans`)).json()).find((plan) => plan.repo && plan.repo.toLowerCase().endsWith("flyout-repo"));
  assert.ok(createdPlan, "the plan was not registered");
  await page.screenshot({ path: join(evidenceRun, "plan-build-prompt.png") });
  await page.locator("#plan-build-yes").click();
  await page.locator("#plan-wizard").waitFor({ state: "visible" });
  // 2gp keeps an empty/skipped planning wizard identical to the established
  // Build-plan behavior exercised by this regression scenario.
  for (let step = 0; step < 3; step += 1) await page.locator("#plan-wizard-skip").click();
  await page.waitForURL((url) => url.pathname === "/chat" && !!url.searchParams.get("session"));
  await page.locator("#chat-task").waitFor({ state: "visible" });
  // v0.70.1 overrule: Yes sends the fixed opening request — one user message,
  // that request, and nothing else in the operator's name; the composer is empty.
  await waitEvent(null, (event) => event.type === "message.appended" && event.data?.message?.role === "user" && String(event.data?.message?.content || "").includes("draft its plan"), "the opening request sent on Yes", 20000);
  const planner = Object.values((await state()).sessions).find((entry) => entry.role === "d" && entry.plan_id === createdPlan.id);
  assert.ok(planner, "Yes did not open a planning chat bound to the plan");
  const opening = (planner.messages || []).filter((message) => message.role === "user");
  assert.equal(opening.length, 1, "Yes sends exactly one message");
  assert.match(opening[0].content, /draft its plan/);
  assert.equal(await page.locator("#chat-task").inputValue(), "", "the request was sent, not left in the composer");
  // Its run holds the shared profile; the queue scenario below starts after it.
  await waitEvent(planner.id, (event) => event.type === "run.stopped", "the planning chat's opening run finished", 30000);
  record("plan-flyout-add-folder-build-prompt-yes-sends");

  // Item 2fc: runs queue per model profile. While one chat holds the profile,
  // another waits with the role it is behind, and Go refuses with the reason.
  const holder = (await json(`http://127.0.0.1:${appPort}/api/sessions`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ agent_id: "acceptance" }) })).session;
  const waiter = (await json(`http://127.0.0.1:${appPort}/api/sessions`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ agent_id: "acceptance" }) })).session;
  const post = async (id, text) => fetch(`http://127.0.0.1:${appPort}/api/message`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ session_id: id, text }) });
  await writeFile(planPath, markedPlan + "- [ ] 2x waits for the model\n");
  await writeFile(join(browserPlanDir, "plan", "items", "2x.md"), "state: live\nverify: echo verified\n\n# 2x\n");
  await post(holder.id, "acceptance: hold the model");
  await waitEvent(holder.id, (event) => event.type === "run.started", "the holder's run started");
  await page.goto(chatURL(waiter.id));
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await post(waiter.id, "acceptance: queued behind");
  await browser.wait(`document.querySelector('#chat-notice')?.innerText.includes('waiting for model · behind agent_b')`, "the strip names the role ahead");
  const busyGo = await (await fetch(`http://127.0.0.1:${appPort}/api/plan/go?plan_id=browser-plan`)).json();
  assert.equal(busyGo.enabled, false, JSON.stringify(busyGo));
  assert.match(busyGo.refusal || "", /the model is busy: agent_b is running/, JSON.stringify(busyGo));
  await page.screenshot({ path: join(evidenceRun, "queued-behind.png") });
  await waitProjectedChatText(waiter.id, "BEHIND ANSWER", "the waiting chat answered once the profile was free", 20000);
  await writeFile(planPath, markedPlan);
  record("per-profile-queue-waiting-behind-and-go-refused");

  // Item 2fs, the walk's reproduction: chat A pauses on a card nobody answers;
  // chat B on the same model runs to its answer meanwhile; answering A's card
  // lets A take the model back and finish.
  const carded = (await json(`http://127.0.0.1:${appPort}/api/sessions`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ agent_id: "acceptance" }) })).session;
  const beside = (await json(`http://127.0.0.1:${appPort}/api/sessions`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ agent_id: "acceptance" }) })).session;
  await post(carded.id, "acceptance: card nobody answers");
  const unanswered = await waitEvent(carded.id, (event) => event.type === "approval.required", "chat A's card");
  await page.goto(chatURL(beside.id));
  await page.locator("#chat-task").waitFor({ state: "visible" });
  await post(beside.id, "acceptance: beside the card");
  await waitProjectedChatText(beside.id, "BESIDE THE CARD ANSWER", "chat B answered while chat A's card waited", 20000);
  assert.equal((await state()).sessions[carded.id].run.status, "paused", "chat A is still waiting on its card");
  await page.screenshot({ path: join(evidenceRun, "beside-the-card.png") });
  let answering = unanswered;
  for (let guard = 0; guard < 4 && answering; guard += 1) {
    await json(`http://127.0.0.1:${appPort}/api/approve`, { method: "POST", headers: { "Content-Type": "application/json", "X-AgentB-Mutation-Token": (await state()).mutation_token }, body: JSON.stringify({ session_id: carded.id, call_id: answering.data.call_id, decision: "once" }) });
    answering = await waitEvent(carded.id, (event) => event.seq > answering.seq && (event.type === "approval.required" || event.type === "run.stopped"), "chat A's next card or its end", 20000);
    if (answering.type === "run.stopped") answering = null;
  }
  await waitProjectedChatText(carded.id, "CARD RELEASED ANSWER", "chat A finished once its card was answered", 20000);
  record("card-nobody-answers-does-not-hold-the-model");

  // Item 2fc: no horizontal scrollbar on the Plan page at any supported width,
  // and no vertical one for a plan that fits.
  for (const width of [900, 1250, 1600]) {
    await page.setViewportSize({ width, height: 975 });
    await page.goto(planPageURL);
    await browser.wait(`document.querySelector('#plan-raw')?.textContent.includes('# Browser plan')`, `the plan at ${width}px`);
    const overflow = await page.evaluate(() => [document.documentElement, ...document.querySelectorAll(".plan-flyout, .plan-list, .plan-view, .plan-body, .plan-raw, .plan-stats")].map((node) => ({ name: node.className || node.tagName, wide: node.scrollWidth - node.clientWidth, tall: node.scrollHeight - node.clientHeight })));
    for (const entry of overflow) assert.ok(entry.wide <= 0, `horizontal overflow at ${width}px: ${JSON.stringify(entry)}`);
    const body = overflow.find((entry) => entry.name === "plan-body");
    assert.ok(body && body.tall <= 0, `a plan that fits scrolls at ${width}px: ${JSON.stringify(body)}`);
  }
  await page.setViewportSize({ width: 1250, height: 975 });
  record("plan-page-no-horizontal-scrollbar-at-supported-widths");

  record("fake-model-script-complete");
  await writeFile(join(evidenceRun, "result.json"), JSON.stringify({ scenarios, duration_ms: Date.now() - startedAt, session_id: sessionID, shell_flip: shellFlipEvidence, shell_style_boundary: shellStyleBoundaryEvidence, composer_family: composerFamilyEvidence }, null, 2));
  const evidenceLogs = join(evidenceRun, "jsonl");
  await mkdir(evidenceLogs, { recursive: true });
  for (const name of (await readdir(join(args.data, "logs"))).filter((item) => item.endsWith(".jsonl"))) {
    await writeFile(join(evidenceLogs, name), await readFile(join(args.data, "logs", name)));
  }
}

if (realModel) record("real-model-script-complete");
process.stdout.write(`CHAT ACCEPTANCE PASS ${Date.now() - startedAt} ms\n`);

await edgeContext?.close();
terminateChildren();
await Promise.all(children.map((child) => waitForChildExit(child)));
await stopFake();
