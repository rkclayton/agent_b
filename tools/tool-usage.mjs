#!/usr/bin/env node
// 2wc: how often the model has called recall, chat_history and remember, read
// from a chats folder given as the argument. Prints tool names, counts, event
// kinds and dates only — never a path, a chat's id or anything a chat says.
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

export const watched = ["recall", "chat_history", "remember"];
const lookups = new Set(["recall", "chat_history"]);
const context = new Set(["tool.call", "tool.result", "model.response", "compaction"]);
const day = (event) => String(event.ts || "").slice(0, 10);

export function readChats(folder) {
  return readdirSync(folder).filter((name) => name.endsWith(".jsonl")).sort().map((name) => {
    const seen = new Set(), events = [];
    for (const line of readFileSync(join(folder, name), "utf8").split(/\r?\n/)) {
      if (!line.trim()) continue;
      let event;
      try { event = JSON.parse(line); } catch { continue; }
      const key = `${event.seq}\0${event.type}`;
      if (!seen.has(key)) { seen.add(key); events.push(event); }
    }
    return events.sort((a, b) => a.seq - b.seq);
  });
}

export function usage(chats) {
  const totals = Object.fromEntries(watched.map((name) => [name, { calls: 0, chats: new Set(), runs: new Set(), first: "", last: "" }]));
  const offered = Object.fromEntries(watched.map((name) => [name, new Set()]));
  const calls = [], runs = new Set();
  let compactions = 0, followed = 0;
  chats.forEach((events, chat) => {
    const byRun = new Map();
    for (const event of events) if (event.run_id) (byRun.get(event.run_id) || byRun.set(event.run_id, []).get(event.run_id)).push(event);
    for (const [run, list] of byRun) {
      const key = `${chat}\0${run}`;
      runs.add(key);
      list.forEach((event, index) => {
        if (event.type === "model.request") for (const name of watched) if ((event.data?.tools_offered || []).includes(name)) offered[name].add(key);
        if (event.type === "compaction") {
          compactions++;
          if (list.slice(index + 1).some((one) => one.type === "tool.call" && lookups.has(one.data?.name))) followed++;
        }
        const name = event.type === "tool.call" ? event.data?.name : "";
        if (!totals[name]) return;
        const total = totals[name], date = day(event);
        total.calls++; total.chats.add(chat); total.runs.add(key);
        if (date && (!total.first || date < total.first)) total.first = date;
        if (date > total.last) total.last = date;
        if (!lookups.has(name)) return;
        const before = list.slice(0, index).reverse().find((one) => context.has(one.type));
        calls.push({ tool: name, date, turn: event.data?.turn ?? "", before: before ? (before.type.startsWith("tool.") ? `${before.type} ${before.data?.name || ""}`.trim() : before.type) : "run start", compactedEarlier: list.slice(0, index).some((one) => one.type === "compaction") });
      });
    }
  });
  return { chats: chats.length, runs: runs.size, totals, offered, calls, compactions, followed };
}

export function tables(result) {
  const lines = [`chats scanned: ${result.chats}; runs scanned: ${result.runs}`, "", "| tool | calls | chats | runs | first | last | runs offered |", "| --- | ---: | ---: | ---: | --- | --- | ---: |"];
  for (const name of watched) { const total = result.totals[name]; lines.push(`| ${name} | ${total.calls} | ${total.chats.size} | ${total.runs.size} | ${total.first || "—"} | ${total.last || "—"} | ${result.offered[name].size} |`); }
  lines.push("", "| # | tool | date | turn | event before | compaction earlier in run |", "| ---: | --- | --- | ---: | --- | :---: |");
  if (!result.calls.length) lines.push("| — | no recall or chat_history call | | | | |");
  result.calls.forEach((call, index) => lines.push(`| ${index + 1} | ${call.tool} | ${call.date} | ${call.turn} | ${call.before} | ${call.compactedEarlier ? "yes" : "no"} |`));
  lines.push("", "| compactions | followed by recall or chat_history in the same run | rate |", "| ---: | ---: | ---: |", `| ${result.compactions} | ${result.followed} | ${result.compactions ? `${(100 * result.followed / result.compactions).toFixed(0)}%` : "—"} |`);
  return lines.join("\n");
}

if (import.meta.url === pathToFileURL(process.argv[1] || "").href) {
  if (!process.argv[2]) { process.stderr.write("usage: node tools/tool-usage.mjs <chats folder>\n"); process.exit(2); }
  process.stdout.write(tables(usage(readChats(process.argv[2]))) + "\n");
}
