import assert from "node:assert/strict";
import test from "node:test";

import { checkBuild, compareWithServer, decideBuildAction } from "./build-check.js";

test("A page from the running build is current", () => {
  assert.equal(decideBuildAction("aaa", "aaa", ""), "current");
});

test("A page from another build reloads once per server build", () => {
  assert.equal(decideBuildAction("old", "new", ""), "reload");
  assert.equal(decideBuildAction("", "new", ""), "reload");
  assert.equal(decideBuildAction("old", "new", "new"), "stale");
  assert.equal(decideBuildAction("old", "newer", "new"), "reload");
});

test("An unreadable server build changes nothing", () => {
  assert.equal(decideBuildAction("old", "", ""), "unknown");
});

function harness(pageBuild, serverBuild, stored = "") {
  const values = new Map(stored ? [["agentb.build-reload", stored]] : []);
  const calls = { reloads: 0, unregistered: 0, fetched: [] };
  return {
    calls,
    values,
    options: {
      doc: { querySelector: () => (pageBuild === null ? null : { content: pageBuild }) },
      nav: { serviceWorker: { getRegistrations: async () => [{ unregister: async () => { calls.unregistered += 1; } }] } },
      storage: { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) },
      fetcher: async (url, init) => { calls.fetched.push([url, init.cache]); return { ok: true, json: async () => ({ build: { executable_sha256: serverBuild } }) }; },
      reload: () => { calls.reloads += 1; },
    },
  };
}

test("A stale page unregisters service workers, reloads once, and does not loop", async () => {
  const first = harness("old", "new");
  assert.equal(await checkBuild(first.options), "reload");
  assert.equal(first.calls.reloads, 1);
  assert.equal(first.calls.unregistered, 1);
  assert.deepEqual(first.calls.fetched, [["/api/state", "no-store"]]);
  const again = harness("old", "new", first.values.get("agentb.build-reload"));
  assert.equal(await checkBuild(again.options), "stale");
  assert.equal(again.calls.reloads, 0);
});

test("A current page clears the reload marker", async () => {
  const current = harness("new", "new", "new");
  assert.equal(await checkBuild(current.options), "current");
  assert.equal(current.calls.reloads, 0);
  assert.equal(current.values.has("agentb.build-reload"), false);
});

test("A snapshot from a restarted server with another build reloads the open page once", () => {
  const values = new Map();
  const storage = { getItem: (key) => values.get(key) ?? null, setItem: (key, value) => values.set(key, value), removeItem: (key) => values.delete(key) };
  let reloads = 0;
  const doc = { querySelector: () => ({ content: "old" }) };
  assert.equal(compareWithServer("new", { doc, storage, reload: () => { reloads += 1; } }), "reload");
  assert.equal(compareWithServer("new", { doc, storage, reload: () => { reloads += 1; } }), "stale");
  assert.equal(reloads, 1);
});

test("Blocked or write-failing storage never reloads, so it cannot loop", () => {
  let reloads = 0;
  const doc = { querySelector: () => ({ content: "old" }) };
  assert.equal(compareWithServer("new", { doc, storage: null, reload: () => { reloads += 1; } }), "stale");
  const readOnly = { getItem: () => null, setItem: () => { throw new Error("quota"); }, removeItem: () => {} };
  assert.equal(compareWithServer("new", { doc, storage: readOnly, reload: () => { reloads += 1; } }), "stale");
  assert.equal(reloads, 0);
});
