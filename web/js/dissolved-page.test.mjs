import assert from "node:assert/strict";
import { readdir, readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);
const js = new URL("./", import.meta.url);
// The word on its own, however it is cased. "consoles" or "console_log" is not
// it; neither is the browser API, which is allowed below by its exact call.
const standalone = /(^|[^a-z0-9_-])console([^a-z0-9_-]|$)/i;
// The browser API keeps the name, and the UI error relay reports which of its
// methods was called, so that one value is a protocol name rather than
// something the operator reads.
const protocol = new Set(["console.error", "console.warn", "console.log"]);

// A plain quote in a comment - an apostrophe - opens a run that this scanner
// reads as a string and that ends at the next apostrophe, swallowing whatever
// lies between. Such a run crosses lines; a real single- or double-quoted
// literal cannot. Dropping those is what stops a comment from failing a test
// about shipped text, which it did in v1.1.2, v1.2.1 and v1.2.2.
function literals(source) {
  const found = [];
  for (let at = 0; at < source.length; at++) {
    const quote = source[at];
    if (quote !== '"' && quote !== "'" && quote !== "`") continue;
    let value = "";
    for (at++; at < source.length; at++) {
      if (source[at] === "\\") { at++; continue; }
      if (quote === "`" && source[at] === "$" && source[at + 1] === "{") {
        let depth = 1;
        at += 2;
        while (at < source.length && depth) {
          if (source[at] === "{") depth++;
          else if (source[at] === "}") depth--;
          at++;
        }
        at--;
        continue;
      }
      if (source[at] === quote) break;
      value += source[at];
    }
    if (quote !== "`" && /[\r\n]/.test(value)) continue;
    found.push(value);
  }
  return found;
}

// Item 2gk (v1.2.3): the page is dissolved, so the word is gone from what the
// operator can read and from the names the shipped code uses for itself. Built
// exactly like the folder-vocabulary test beside it.
test("no shipped string says Console", async () => {
  const names = await readdir(root);
  const jsNames = (await readdir(js)).filter((name) => name.endsWith(".js") && !name.includes("test"));
  for (const name of jsNames) {
    const source = await readFile(new URL(name, js), "utf8");
    for (const value of literals(source)) {
      if (protocol.has(value.trim())) continue;
      assert.doesNotMatch(value, standalone, `${name}: ${value}`);
    }
    // Nothing in the shipped code is NAMED after the page either - no
    // mountConsole, no consoleStop. The bare global keeps its own name.
    for (const identifier of source.match(/[A-Za-z0-9_]*console[A-Za-z0-9_]*/gi) || []) {
      assert.equal(identifier, "console", `${name}: ${identifier}`);
    }
  }
  for (const name of names.filter((name) => name.endsWith(".html"))) {
    const source = await readFile(new URL(`../${name}`, import.meta.url), "utf8");
    const text = source.replace(/<script[\s\S]*?<\/script>/gi, "").replace(/<style[\s\S]*?<\/style>/gi, "").replace(/<!--[\s\S]*?-->/g, " ").replace(/<[^>]+>/g, " ");
    assert.doesNotMatch(text, standalone, name);
  }
});

// The page, its route and its entry in the tab menu are gone, and what it held
// is reachable in its new home.
test("the page, its route and its tab-menu entry are gone", async () => {
  const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
  const shell = await readFile(new URL("./shell.js", js), "utf8");
  const workspace = await readFile(new URL("./workspace.js", js), "utf8");
  const settings = await readFile(new URL("./settings.js", js), "utf8");

  assert.doesNotMatch(html, /id="panel-surface"/);
  assert.doesNotMatch(workspace, /switchView: \(next/);
  assert.doesNotMatch(shell, /flip\.label|flip\.open/);

  // Every group it held has a home: two sections of Settings, and the chat.
  assert.match(settings, /\["agents", "Agents"\]/);
  assert.match(settings, /\["activity", "Activity"\]/);
  for (const id of ["panel-agent", "panel-tools", "panel-tools-count"]) {
    assert.match(html, new RegExp(`id="${id}"[\\s\\S]*id="activity-panel"`), id);
  }
  for (const id of ["panel-tool-counters", "panel-stats", "panel-live", "panel-run-label", "rail", "flow", "rack", "state-list", "timeline-list", "panel-reflection-overview", "panel-reflection-report", "clear-stats", "flush-memory"]) {
    assert.match(html, new RegExp(`id="activity-panel"[\\s\\S]*id="${id}"`), id);
  }
  assert.match(html, /id="chat-status-strip"[\s\S]*id="chat-attach"/);
  assert.doesNotMatch(html, /id="chat-readout"/);
});
