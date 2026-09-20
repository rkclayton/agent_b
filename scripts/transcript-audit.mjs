// Item 2gg, v1.1.2/W3 — the transcript as a record, audited before anything is
// changed. The claim under test is that the rendered transcript is a pure
// function of the journal: the projector folds a chat's JSONL into
// `session.chat` entries, each with a stable key, and the renderer draws one
// row per entry carrying that key in `data-entry-key`. If the render is a pure
// function of the journal then those two sequences agree, entry for entry, in
// order, after a restart, after compaction and after a reload.
//
//   node scripts/transcript-audit.mjs --exe <exe> --app-root <dir> --data <dir> \
//     --evidence <dir> [--journals <dir>] [--operator-journals <dir>] [--synthetic 2000]
//
// The operator's own journals are read ONLY by copying them into the disposable
// data root; nothing writes to his data root and nothing is sent anywhere.
// The audit asserts nothing: it reports mismatches, which is what W3 owes.
import assert from "node:assert/strict";
import { copyFile, mkdir, readdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { basename, join, resolve } from "node:path";
import { start, waitFor } from "./ui-harness.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const evidence = resolve(args.evidence);
await mkdir(evidence, { recursive: true });
const syntheticSize = Number(args.synthetic || 2000);

const mismatches = [];
const observations = [];
function mismatch(chat, kind, detail) {
  mismatches.push({ chat, kind, ...detail });
  process.stdout.write(`MISMATCH ${chat} ${kind} ${JSON.stringify(detail).slice(0, 400)}\n`);
}
function observe(chat, note, detail) {
  observations.push({ chat, note, ...detail });
  process.stdout.write(`note ${chat} ${note} ${JSON.stringify(detail).slice(0, 300)}\n`);
}

// A synthetic chat of `count` messages, written as a journal the harness
// restores like any other. It exists to measure a transcript nobody has ever
// scrolled: 2,000 messages is past anything the walk produced.
function syntheticJournal(sessionID, count) {
  const lines = [];
  let seq = 1;
  const at = (index) => new Date(Date.UTC(2026, 8, 20, 0, 0, 0) + index * 1000).toISOString();
  const push = (type, data, runID = "") => lines.push(JSON.stringify({ seq: seq++, at: at(seq), session_id: sessionID, run_id: runID, type, data }));
  // The shapes are the ones a real journal carries (logs/evidence/chats):
  // session.created nests the whole session, and a run is message.appended,
  // run.started, model.request, model.response, run.stopped.
  push("session.created", {
    session: {
      id: sessionID, label: "synthetic", agent_id: "ui", server_id: "ui", agent_name: "UI", b_profile: "ui",
      role: "b", created_at: at(0), closed: false, name_pinned: false, workspace: "",
    },
  });
  for (let index = 0; index < count / 2; index++) {
    const runID = `r${index}`;
    const messageID = `m-${index}`;
    push("message.appended", { message: { id: messageID, role: "user", content: `synthetic question ${index} about a distinctive-token-${index}` } }, runID);
    push("run.started", { run_id: runID, user_message_id: messageID }, runID);
    push("model.request", { est_prompt_tokens: 10, estimated: true, message_count: 2 }, runID);
    push("model.response", { content: `synthetic answer ${index}`, finish_reason: "stop", duration_ms: 10 }, runID);
    push("run.stopped", { run_id: runID, reason: "done", turns: 1, queue_held: false }, runID);
  }
  return lines.join("\n") + "\n";
}

// The two sequences. `entries` is what the projector made of the journal;
// `rendered` is what the page drew. Compared by key, in order.
async function compare(page, sessionID, getState, label) {
  const state = await getState();
  const session = state.sessions[sessionID];
  const entries = (session?.chat ?? []).map((entry) => ({ key: entry.key, type: entry.type }));
  const rendered = await page.evaluate(() => [...document.querySelectorAll("#chat-log [data-entry-key]")].map((node) => ({
    key: node.dataset.entryKey,
    className: node.className,
    text: (node.innerText || "").slice(0, 80),
  })));
  // A key may legitimately carry more than one row (a turn's prose and its
  // steps share the turn's key), so the comparison is over the ORDER of
  // distinct keys, which is what "same journal, same render" means.
  const renderedKeys = [];
  for (const row of rendered) if (renderedKeys[renderedKeys.length - 1] !== row.key) renderedKeys.push(row.key);
  const entryKeys = entries.map((entry) => entry.key);
  const missing = entryKeys.filter((key) => !renderedKeys.includes(key));
  const extra = renderedKeys.filter((key) => !entryKeys.includes(key));
  const shared = entryKeys.filter((key) => renderedKeys.includes(key));
  const sharedRendered = renderedKeys.filter((key) => entryKeys.includes(key));
  const orderOK = shared.every((key, index) => sharedRendered[index] === key);
  const result = { label, entries: entries.length, rendered_rows: rendered.length, rendered_keys: renderedKeys.length, missing: missing.length, extra: extra.length, order_preserved: orderOK };
  if (missing.length) {
    // Name the entry AND the journal shape it came from, which is what the
    // item asks for: the journal line beside the render.
    result.missing_sample = missing.slice(0, 8).map((key) => ({ key, type: entries.find((entry) => entry.key === key)?.type }));
  }
  if (extra.length) result.extra_sample = extra.slice(0, 8);
  return result;
}

// The journals are staged BEFORE anything starts, so the harness's own first
// chat never claims an id one of them needs and never holds a file open.
const dataRoot = resolve(args.data);
const report = { schema: 1, audited: [], mismatches, observations };
{
  // Retained chats live beside the log directory, in <data root>/chats — that
  // is the directory the server restores from (events.Writers.DurableChatPaths).
  const logDir = join(dataRoot, "chats");
  await mkdir(logDir, { recursive: true });

  // A chat comes back under the id its OWN journal names, not under the file
  // name, and two journals naming one id abort the whole startup, so a second
  // claimant of an id is skipped and said so.
  const claimed = new Set();
  const sessionIDOf = async (path) => {
    const text = await readFile(path, "utf8").catch(() => "");
    for (const line of text.split("\n")) {
      if (!line.includes("session.created")) continue;
      try {
        const data = JSON.parse(line)?.data;
        const id = data?.session?.id ?? data?.session_id;
        if (id) return String(id);
      } catch { /* keep looking */ }
    }
    return "";
  };
  for (const name of await readdir(logDir).catch(() => [])) {
    if (name.endsWith(".jsonl")) await rm(join(logDir, name), { force: true });
  }

  // The chats to audit: the walk's retained journal, a synthetic 2,000-message
  // chat, and — read-only, by copy — the newest of the operator's.
  const staged = [];
  async function stage(sourcePath, source, extra = {}) {
    const id = await sessionIDOf(sourcePath);
    if (!id) return void observe(basename(sourcePath), "journal names no session", { path: sourcePath });
    if (claimed.has(id)) return void observe(id, "a second journal claims this session id; skipped", { path: sourcePath });
    claimed.add(id);
    await copyFile(sourcePath, join(logDir, `${id}.jsonl`));
    staged.push({ id, source, path: sourcePath, ...extra });
  }
  const walkJournals = resolve(args.journals || "logs/evidence/chats");
  for (const name of await readdir(walkJournals).catch(() => [])) {
    if (name.endsWith(".jsonl")) await stage(join(walkJournals, name), "walk");
  }
  const syntheticID = "synthetic-2000";
  await writeFile(join(logDir, `${syntheticID}.jsonl`), syntheticJournal(syntheticID, syntheticSize));
  claimed.add(syntheticID);
  staged.push({ id: syntheticID, source: "synthetic", path: "(generated)" });

  if (args["operator-journals"]) {
    const operatorDir = resolve(args["operator-journals"]);
    const names = (await readdir(operatorDir).catch(() => [])).filter((name) => name.endsWith(".jsonl"));
    const sized = [];
    for (const name of names) {
      const info = await stat(join(operatorDir, name)).catch(() => null);
      if (info) sized.push({ name, bytes: info.size, at: info.mtimeMs });
    }
    // The three newest that actually hold a conversation, copied in. His data
    // root is never written to and never read beyond these copies.
    const chosen = sized.filter((entry) => entry.bytes > 4096).sort((a, b) => b.at - a.at).slice(0, 3);
    for (const entry of chosen) await stage(join(operatorDir, entry.name), "operator (read-only copy)", { bytes: entry.bytes });
  }
  report.staged = staged;

  // The harness restores retained chats at start, so starting it with the
  // journals already in place IS the "after a restart" condition.
  // A retained chat names the agent it was opened with. The operator's chats
  // name agents this disposable install has never heard of, and restore says
  // so, so the audit configures every agent its staged journals reference.
  const agentNames = new Set();
  for (const entry of staged) {
    const text = await readFile(join(logDir, entry.id + ".jsonl"), "utf8").catch(() => "");
    for (const line of text.split(String.fromCharCode(10))) {
      if (!line.includes(String.fromCharCode(34)+"agent_id"+String.fromCharCode(34))) continue;
      try {
        const value = JSON.parse(line)?.data?.agent_id;
        if (value) agentNames.add(String(value));
      } catch { /* a partial last line names no agent */ }
    }
  }
  report.agents_configured = [...agentNames];
  const restarted = await start({ exe: args.exe, appRoot: args["app-root"], data: dataRoot, reachable: true, readyTimeout: 180000, agents: [...agentNames] });
  try {
    const state = await restarted.getState();
    const restored = Object.keys(state.sessions);
    report.restored_sessions = restored;
    const page = await restarted.context.newPage();
    const pageErrors = [];
    page.on("pageerror", (error) => pageErrors.push(String(error.message)));

    for (const entry of staged) {
      const sessionID = restored.find((id) => id === entry.id);
      if (!sessionID) {
        mismatch(entry.id, "not-restored", { source: entry.source, note: "the journal was in the log directory and the session did not come back" });
        continue;
      }
      await page.goto(`${restarted.base}/chat?session=${sessionID}`);
      await page.locator("#chat-task").waitFor({ state: "visible" });
      await waitFor(async () => (await page.evaluate(() => document.querySelectorAll("#chat-log [data-entry-key]").length)) > 0, `${sessionID} drew its transcript`, 20000).catch(() => {});
      await page.waitForTimeout(600);

      const afterRestart = await compare(page, sessionID, restarted.getState, "after restart");
      const audited = { chat: sessionID, source: entry.source, after_restart: afterRestart };
      if (afterRestart.missing || afterRestart.extra || !afterRestart.order_preserved) mismatch(sessionID, "render-differs-from-journal", afterRestart);

      // After a reload: the same journal, so the same render.
      await page.reload();
      await page.locator("#chat-task").waitFor({ state: "visible" });
      await page.waitForTimeout(800);
      const afterReload = await compare(page, sessionID, restarted.getState, "after reload");
      audited.after_reload = afterReload;
      if (afterReload.rendered_keys !== afterRestart.rendered_keys) mismatch(sessionID, "reload-changes-the-render", { before: afterRestart.rendered_keys, after: afterReload.rendered_keys });

      // Copy: ordered plain text with the kinds marked, and no UI chrome.
      const copied = await page.evaluate(() => {
        const log = document.getElementById("chat-log");
        if (!log) return null;
        const selection = window.getSelection();
        const range = document.createRange();
        range.selectNodeContents(log);
        selection.removeAllRanges();
        selection.addRange(range);
        const text = selection.toString();
        selection.removeAllRanges();
        return text;
      });
      audited.copy = copied === null ? { available: false } : {
        available: true,
        characters: copied.length,
        lines: copied.split("\n").length,
        marks_you: /(^|\n)\s*you:/i.test(copied),
        marks_agent: /(^|\n)\s*agent_b:/i.test(copied),
        marks_tool: /—\s*tool\s*—/i.test(copied),
        sample: copied.slice(0, 240),
      };
      if (audited.copy.available && !(audited.copy.marks_you && audited.copy.marks_agent)) {
        mismatch(sessionID, "copy-has-no-kind-marks", { marks_you: audited.copy.marks_you, marks_agent: audited.copy.marks_agent, marks_tool: audited.copy.marks_tool, sample: audited.copy.sample });
      }

      // Browser find: the transcript must be real DOM text, including inside
      // stubs and summaries, because no search box is being added.
      const findable = await page.evaluate(() => {
        const log = document.getElementById("chat-log");
        const inside = (selector) => [...log.querySelectorAll(selector)].map((node) => (node.innerText || "").trim()).filter(Boolean);
        return {
          stub_selectors: inside("[class*='stub']").length,
          summary_selectors: inside("[class*='summary']").length,
          stub_text: inside("[class*='stub']").slice(0, 2),
          summary_text: inside("[class*='summary']").slice(0, 2),
          hidden_text_nodes: [...log.querySelectorAll("*")].filter((node) => {
            const style = getComputedStyle(node);
            return (style.display === "none" || style.visibility === "hidden") && (node.textContent || "").trim().length > 0;
          }).length,
        };
      });
      audited.find = findable;
      if (findable.hidden_text_nodes > 0) observe(sessionID, "text the browser's find cannot reach", { hidden_text_nodes: findable.hidden_text_nodes });

      report.audited.push(audited);
    }
    report.page_errors = pageErrors;
    await page.close();
  } finally {
    await restarted.stop();
  }
}

await writeFile(join(evidence, "w3-transcript-audit.json"), JSON.stringify(report, null, 2));
process.stdout.write(`TRANSCRIPT AUDIT — ${report.audited.length} chats, ${mismatches.length} mismatches, ${observations.length} notes\n`);
await rm(resolve(args.data), { recursive: true, force: true }).catch(() => {});
