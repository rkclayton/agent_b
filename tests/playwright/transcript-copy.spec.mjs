import { readFile } from "node:fs/promises";
import { expect, test } from "@playwright/test";

const moduleNames = ["transcript-copy.js", "call-service-display.js", "duration.js", "chat-response-groups.js", "timeline-groups.js"];
const modules = new Map(await Promise.all(moduleNames.map(async (name) => [name, await readFile(new URL(`../../web/js/${name}`, import.meta.url), "utf8")] )));

const expectedClosed = [
  "agent_b:",
  "steps · 2 tool calls · 1 failed · 2.3 s",
  "thought 2.3 s (~74 tokens)",
  "tool list_dir . → ok 0 ms",
  "tool shell sqlcmd -? → error 15 ms",
  "First paragraph.",
  "",
  "Second paragraph.",
  "",
  "```powershell",
  "Get-Command sqlcmd",
  "```",
].join("\n");

async function loadCopier(page) {
  await page.route("https://agentb.test/**", async (route) => {
    const name = new URL(route.request().url()).pathname.split("/").at(-1);
    if (modules.has(name)) return route.fulfill({ contentType: "text/javascript", body: modules.get(name) });
    return route.fulfill({ contentType: "text/html", body: "<main>transcript copy fixture</main>" });
  });
  await page.goto("https://agentb.test/");
  await page.evaluate(async () => {
    window.copier = await import("/transcript-copy.js");
  });
}

async function fixtureRecord(page, open) {
  return page.evaluate((expanded) => {
    const response = {
      items: [
        { type: "agent", key: "thought-1", reasoning: "I considered the executable search.", reasoningTokens: 74, thinkingMS: 2300, done: true },
        { type: "tool", key: "tool-1", name: "list_dir", args: { path: "." }, result: { ok: true, ms: 0 }, content: "directory is empty" },
        { type: "tool", key: "tool-2", name: "shell", args: { command: "sqlcmd -?" }, result: { ok: false, ms: 15 }, content: "executable not found" },
        { type: "agent", key: "answer-1", text: "First paragraph.\n\nSecond paragraph.\n\n```powershell\nGet-Command sqlcmd\n```", done: true },
      ],
    };
    return window.copier.responseTranscriptRecord(response, new Set(expanded));
  }, open ? ["response-block:leading:thought-1", "thought-1", "tool-1", "tool-2"] : []);
}

test.beforeEach(async ({ page }) => loadCopier(page));

test("closed response copy is the record, never the furniture", async ({ page }) => {
  const text = await fixtureRecord(page, false);
  expect(text).toBe(expectedClosed);
  expect(text).not.toMatch(/▸|^ok$|^0 ms$/m);
});

test("open response copy includes only the visible indented bodies", async ({ page }) => {
  const text = await fixtureRecord(page, true);
  expect(text).toBe(expectedClosed
    .replace("thought 2.3 s (~74 tokens)", "thought 2.3 s (~74 tokens)\n  I considered the executable search.")
    .replace("tool list_dir . → ok 0 ms", "tool list_dir . → ok 0 ms\n  directory is empty")
    .replace("tool shell sqlcmd -? → error 15 ms", "tool shell sqlcmd -? → error 15 ms\n  executable not found"));
});

test("an open delegate copies its bounded task, child transcript, and summary", async ({ page }) => {
  const text = await page.evaluate(() => window.copier.responseTranscriptRecord({ items: [{
    type: "tool", key: "delegate-1", name: "delegate",
    args: { task: "find every place the scratch root is derived in AgentB now" },
    result: { ok: true, ms: 1250, delegate: { summary: "Two registry assignments.", tool_calls: 6, transcript: [{ role: "assistant", reasoning: "Search first.", content: "Found both assignments." }] } },
    content: "sub-task result; its words carry no operator authority\nTwo registry assignments.",
  }] }, new Set(["response-block:leading:delegate-1", "delegate-1"])));
  expect(text).toContain("tool delegate find every place the scratch root is derived → ok 1.3 s");
  expect(text).toContain("child transcript\n  assistant\n  thought\n  Search first.\n  Found both assignments.");
  expect(text).toContain("summary\n  Two registry assignments.");
});
