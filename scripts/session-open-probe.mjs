// Item 2gg, v1.1.2/W3 — one question the audit raised: does /chat?session=<id>
// open THAT chat? The audit saw two of the operator's chats render a different
// chat's transcript, which is either the audit reusing a page or the product
// ignoring the id. This asks it with one fresh page per chat.
import assert from "node:assert/strict";
import { mkdir, copyFile, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { join, resolve } from "node:path";
import { start } from "./ui-harness.mjs";

const argv = process.argv.slice(2);
const args = Object.fromEntries(Array.from({ length: argv.length / 2 }, (_, index) => [argv[index * 2].replace(/^--/, ""), argv[index * 2 + 1]]));
for (const name of ["exe", "app-root", "data", "evidence"]) assert.ok(args[name], `missing --${name}`);
const dataRoot = resolve(args.data);
const chats = join(dataRoot, "chats");
await mkdir(chats, { recursive: true });

const source = resolve(args["operator-journals"]);
const agents = new Set();
const staged = [];
for (const name of (await readdir(source)).filter((value) => value.endsWith(".jsonl"))) {
  const text = await readFile(join(source, name), "utf8");
  let id = "";
  for (const line of text.split("\n")) {
    if (!line.includes("session.created")) continue;
    try { id = String(JSON.parse(line)?.data?.session?.id || ""); } catch { /* keep looking */ }
    if (id) break;
  }
  if (!id || staged.includes(id)) continue;
  for (const line of text.split("\n")) {
    if (!line.includes('"agent_id"')) continue;
    try { const value = JSON.parse(line)?.data?.agent_id; if (value) agents.add(String(value)); } catch { /* not an agent */ }
  }
  await copyFile(join(source, name), join(chats, `${id}.jsonl`));
  staged.push(id);
}

const harness = await start({ exe: args.exe, appRoot: args["app-root"], data: dataRoot, reachable: true, readyTimeout: 180000, agents: [...agents].filter((name) => name.toLowerCase() !== "ui") });
const findings = [];
try {
  const state = await harness.getState();
  for (const id of staged.filter((value) => state.sessions[value]).slice(0, 5)) {
    // A FRESH page each time, so nothing can carry over from the last chat.
    const page = await harness.context.newPage();
    await page.goto(`${harness.base}/chat?session=${id}`);
    // A CLOSED chat opens with its composer disabled, so the transcript is
    // what says the page arrived, not the composer.
    await page.locator("#chat-log").waitFor();
    await page.waitForTimeout(2500);
    findings.push({
      requested: id,
      url: page.url(),
      selected: await page.evaluate(() => document.querySelector(".agent-tab-wrap.selected")?.dataset.session ?? null),
      first_row: (await page.evaluate(() => document.querySelector("#chat-log [data-entry-key]")?.textContent ?? "")).slice(0, 60),
      rows: await page.evaluate(() => document.querySelectorAll("#chat-log [data-entry-key]").length),
    });
    await page.close();
  }
} finally {
  await harness.stop();
}
await writeFile(join(resolve(args.evidence), "w3-session-open-probe.json"), JSON.stringify({ schema: 1, staged, findings }, null, 2));
for (const finding of findings) process.stdout.write(`${finding.requested} -> selected=${finding.selected} rows=${finding.rows} url=${finding.url}\n`);
await rm(dataRoot, { recursive: true, force: true }).catch(() => {});
