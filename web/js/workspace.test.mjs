import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
const workspace = await readFile(new URL("./workspace.js", import.meta.url), "utf8");
const bus = await readFile(new URL("./bus.js", import.meta.url), "utf8");
const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const panels = await readFile(new URL("./app.js", import.meta.url), "utf8");
const settings = await readFile(new URL("./settings.js", import.meta.url), "utf8");
const server = await readFile(new URL("../../internal/web/server_state.go", import.meta.url), "utf8");

// Item 2gk: one surface is served from this document now. The six groups that
// were a page of their own are two sections of Settings, and both routes that
// used to choose between two pages land on the chat.
test("the served document holds one surface and both routes reach it", () => {
  assert.match(html, /id="chat-log"/);
  assert.match(html, /id="panel-sources"/);
  assert.match(html, /id="agents-panel"/);
  assert.match(html, /id="activity-panel"/);
  assert.match(html, /src="\/static\/js\/workspace\.js"/);
  assert.match(server, /r\.URL\.Path == "\/chat"[\s\S]*name = "index\.html"/);
  assert.doesNotMatch(workspace, /location\.assign|location\.replace|location\.reload/);
  assert.match(workspace, /window\.history\.pushState/);
  assert.match(workspace, /window\.addEventListener\("popstate"/);
});

test("view modules preserve state while unmounted and the bus owns one stream", () => {
  assert.match(chat, /export function mountChat/);
  assert.match(chat, /export function unmountChat/);
  assert.match(panels, /export function mountPanels/);
  assert.match(panels, /export function unmountPanels/);
  assert.doesNotMatch(workspace, /replaceChildren|innerHTML|new EventSource/);
  assert.equal((bus.match(/new EventSource\(/g) || []).length, 1);
  assert.match(bus, /const listeners = new Set\(\)/);
});

// The panels are mounted by the sheet that adopts them, not by a route, and
// they are moved rather than rebuilt so their controls survive a re-render.
test("Settings mounts the two adopted panels and moves them home again", () => {
  assert.match(settings, /\["agents", "Agents"\]/);
  assert.match(settings, /\["activity", "Activity"\]/);
  assert.match(settings, /mountPanels\(\)/);
  assert.match(settings, /unmountPanels\(\)/);
  assert.match(settings, /function returnAdoptedPanels/);
  assert.match(workspace, /mountChat\(shell\)/);
});
