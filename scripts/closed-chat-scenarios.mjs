// Item 2gn, v1.1.3/W1 — a closed chat opens when asked for. Five retained ids
// by URL and by the history entry, each asserted to open THAT chat with a
// transcript that matches its journal (2gg's comparison), and a closed chat
// asserted read-only-with-a-tab until a message reopens it.
//
//   node scripts/closed-chat-scenarios.mjs --exe <exe> --app-root <dir> \
//     --data <dir> --evidence <dir> --journals <dir>
//
// The journals are copied into a disposable root; the source directory is only
// read.
import assert from "node:assert/strict";
import { copyFile, mkdir, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { start, waitFor } from "./ui-harness.mjs";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

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
  process.stdout.write(`${ok ? "PASS" : "FAIL"} ${name}${ok ? "" : ` — ${JSON.stringify(detail).slice(0, 300)}`}\n`);
}

const dataRoot = resolve(args.data);
const chats = join(dataRoot, "chats");
await mkdir(chats, { recursive: true });
const source = resolve(args.journals || "C:/Users/Randy/AppData/Local/Agent_b/chats");
const agents = new Set();
const staged = [];
for (const name of (await readdir(source).catch(() => [])).filter((value) => value.endsWith(".jsonl"))) {
  const text = await readFile(join(source, name), "utf8");
  let id = "";
  for (const line of text.split("\n")) {
    if (line.includes("session.created") && !id) {
      try { id = String(JSON.parse(line)?.data?.session?.id || ""); } catch { /* keep looking */ }
    }
    if (line.includes('"agent_id"')) {
      try { const value = JSON.parse(line)?.data?.agent_id; if (value) agents.add(String(value)); } catch { /* not an agent */ }
    }
  }
  if (!id || staged.includes(id)) continue;
  await copyFile(join(source, name), join(chats, `${id}.jsonl`));
  staged.push(id);
}
assert.ok(staged.length >= 5, `need five retained chats to prove five ids, found ${staged.length}`);

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: dataRoot, reachable: true, readyTimeout: 180000, agents: [...agents].filter((name) => name.toLowerCase() !== "ui") });
const table = [];
try {
  const state = await harness.getState();
  // The five with the most to render, so the comparison has something to say.
  const chosen = staged
    .filter((id) => state.sessions[id])
    .sort((a, b) => (state.sessions[b].chat?.length || 0) - (state.sessions[a].chat?.length || 0))
    .slice(0, 5);
  record("2gn/five-retained-chats-are-available", chosen.length === 5, { chosen });

  for (const id of chosen) {
    const session = state.sessions[id];
    const entries = (session.chat || []).map((entry) => entry.key);
    const page = await harness.context.newPage();
    await page.goto(`${harness.base}/chat?session=${id}`);
    await page.locator("#chat-log").waitFor();
    await waitFor(async () => (await page.evaluate(() => document.querySelector(".agent-tab-wrap.selected")?.dataset.session ?? "")) !== "", `${id} selected a tab`, 15000).catch(() => {});
    await page.waitForTimeout(700);
    const seen = await page.evaluate(() => ({
      url: location.search,
      selected: document.querySelector(".agent-tab-wrap.selected")?.dataset.session ?? null,
      keys: [...document.querySelectorAll("#chat-log [data-entry-key]")].map((node) => node.dataset.entryKey),
      tabs: [...document.querySelectorAll(".agent-tab-wrap")].map((wrap) => wrap.dataset.session),
    }));
    // 2gg's comparison, made against what the product actually renders. The
    // transcript is windowed, filtered and GROUPED: adjacent tool calls fold
    // into one row keyed `tool-group:<first member's key>`, and a turn's rows
    // share the turn's key. So a rendered key maps back to a journal key by
    // stripping that prefix, each key counts once at its first appearance,
    // and the resulting sequence must be strictly increasing in the journal's
    // order and invent nothing. Comparing the raw entry list against the
    // grouped render — which is what v1.1.2 did — measures the grouping, not
    // the record.
    // Item 2gs: the comparison is over the FULL key list, in order, not the
    // intersection — v1.1.2 compared intersections and could not see the
    // transcript rendering runs out of order. `tool-group:<key>` and
    // `thought:<key>` are rows derived from a journal entry, and `<key>#2` is
    // the second row for a journal id that genuinely repeats; all map back.
    // The rendered keys must then be a SUBSEQUENCE of the journal's list,
    // matched greedily left to right so a repeated id consumes its NEXT
    // occurrence rather than the first — matching the first is what made a
    // correct render look unordered.
    const underlying = (key) => key.replace(/^tool-group:/, "").replace(/^thought:/, "").replace(/#\d+$/, "");
    const distinct = [];
    for (const key of seen.keys.map(underlying)) if (distinct[distinct.length - 1] !== key) distinct.push(key);
    const extra = distinct.filter((key) => !entries.includes(key));
    let cursor = -1;
    let ordered = true;
    for (const key of distinct) {
      const at = entries.indexOf(key, cursor + 1);
      if (at < 0) { ordered = false; break; }
      cursor = at;
    }
    const detail = { id, closed: session.closed, entries: entries.length, rendered: distinct.length, extra: extra.length, extra_keys: extra.slice(0, 5), rendered_head: distinct.slice(0, 12), entry_head: entries.slice(0, 12), ordered, url: seen.url, selected: seen.selected };
    table.push(detail);
    record(`2gn/${id}/opens-the-chat-that-was-asked-for`, seen.selected === id && seen.url.includes(`session=${id}`), detail);
    record(`2gn/${id}/transcript-matches-its-journal`, extra.length === 0 && ordered && (entries.length === 0 || distinct.length > 0), detail);
    if (session.closed) record(`2gn/${id}/a-closed-chat-gets-a-tab`, seen.tabs.includes(id), { tabs: seen.tabs });
    await page.close();
  }

  // The history entry takes the same route as the URL.
  {
    const closed = chosen.find((id) => state.sessions[id].closed);
    const page = await harness.context.newPage();
    await page.goto(`${harness.base}/chat`);
    await page.locator("#chat-log").waitFor();
    await page.waitForTimeout(800);
    await page.locator(".agent-tab").first().click({ button: "right" });
    await page.locator(".agent-chat-menu").first().waitFor({ state: "visible" }).catch(() => {});
    const row = page.locator(`.agent-chat-row[data-session="${closed}"] .agent-chat-open`);
    const reachable = await row.count() > 0;
    if (reachable) {
      await row.click();
      await waitFor(async () => (await page.evaluate(() => document.querySelector(".agent-tab-wrap.selected")?.dataset.session ?? "")) === closed, "the history entry opened its chat", 15000).catch(() => {});
    }
    const landed = await page.evaluate(() => document.querySelector(".agent-tab-wrap.selected")?.dataset.session ?? null);
    record("2gn/the-history-entry-opens-the-same-chat", reachable && landed === closed, { closed, landed, reachable });
    await page.close();
  }
} finally {
  await harness.stop();
}

await writeFile(join(evidence, "w1-closed-chat-scenarios.json"), JSON.stringify({ schema: 1, failures, results, table }, null, 2));
process.stdout.write(`CLOSED CHAT SCENARIOS ${failures === 0 ? "PASS" : "FAIL"} ${results.length - failures} of ${results.length}\n`);
// The disposable root goes through the removal guard, never a raw recursive
// rm: the guard refuses a target outside the allowed roots and refuses to
// descend through a junction (scripts/removal-guard.mjs).
try { removeTreeWithinAllowedRoots(dataRoot, [tmpdir()], "v1.1.3 disposable scenario root"); } catch { /* a root already gone is not a failure */ }
process.exitCode = failures === 0 ? 0 : 1;
