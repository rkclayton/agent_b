import test from "node:test";
import assert from "node:assert/strict";
import { arrangeChats, menuLabels, panelDrag } from "./chat-list.js";

test("chat list is pinned, folders, then root; newest first and workers absent 2qz", () => {
  const sessions = {
    p1: { id: "p1", role: "b", last_activity: "2026-10-06T04:00:00Z" },
    p2: { id: "p2", role: "d", last_activity: "2026-10-06T05:00:00Z" },
    f1: { id: "f1", role: "b", last_activity: "2026-10-06T03:00:00Z" },
    f2: { id: "f2", role: "b", last_activity: "2026-10-06T02:00:00Z", closed: true },
    r1: { id: "r1", role: "d", last_activity: "2026-10-06T02:30:00Z" },
    r2: { id: "r2", role: "b", created_at: "2026-10-06T01:00:00Z" },
    worker: { id: "worker", role: "c", last_activity: "2026-10-06T06:00:00Z" },
  };
  const tree = { folders: ["Work"], chats: [
    { id: "p1", folder: "Work", pinned: true }, { id: "p2", folder: "", pinned: true },
    { id: "f1", folder: "Work" }, { id: "f2", folder: "Work" },
    { id: "r1", folder: "" }, { id: "r2", folder: "" }, { id: "worker", folder: "" },
  ] };
  const list = arrangeChats(sessions, tree);
  assert.deepEqual(list.pinned.map((chat) => chat.id), ["p2", "p1"]);
  assert.deepEqual(list.folders[0].chats.map((chat) => chat.id), ["f1", "f2"]);
  assert.deepEqual(list.root.map((chat) => chat.id), ["r1", "r2"]);
  sessions.r2.last_activity = "2026-10-06T06:00:00Z"; assert.deepEqual(arrangeChats(sessions, tree).root.map((chat) => chat.id), ["r2", "r1"]);
  assert.deepEqual([...list.pinned, ...list.folders.flatMap((group) => group.chats), ...list.root].map((chat) => chat.id).sort(), ["f1", "f2", "p1", "p2", "r1", "r2"]);
});

test("row menu is exactly pin rename move delete in order 2qz", () => {
  assert.deepEqual(menuLabels(false), ["Pin", "Rename", "Move to folder", "Archive", "Delete"]);
  assert.deepEqual(menuLabels(true), ["Unpin", "Rename", "Move to folder", "Archive", "Delete"]);
});

test("archived chats are omitted from every list group including pinned 2xt", () => {
  const sessions = { archived: { id:"archived", role:"b", last_activity:"2026-11-10T00:00:00Z" }, kept: { id:"kept", role:"b", last_activity:"2026-11-09T00:00:00Z" } };
  const arranged = arrangeChats(sessions, { folders:["Work"], chats:[{ id:"archived", folder:"Work", pinned:true, archived:true }, { id:"kept", folder:"", pinned:false }] });
  assert.deepEqual(arranged.pinned, []); assert.deepEqual(arranged.folders[0].chats, []); assert.deepEqual(arranged.recent.map(({id})=>id), ["kept"]);
});

test("Recent keeps twenty newest roots plus an older active chat 2xs", () => {
  const sessions = {};
  const chats = [];
  for (let index = 0; index < 30; index++) {
    const id = `root-${index}`;
    sessions[id] = { id, role: "b", last_activity: `2026-11-${String(30 - index).padStart(2, "0")}T00:00:00Z`, run: { status: index === 24 ? "running" : "idle" } };
    chats.push({ id, folder: "", pinned: false });
  }
  sessions.pinned = { id: "pinned", role: "b", last_activity: "2026-10-01T00:00:00Z" };
  sessions.foldered = { id: "foldered", role: "b", last_activity: "2026-10-01T00:00:00Z" };
  chats.push({ id: "pinned", folder: "", pinned: true }, { id: "foldered", folder: "Work", pinned: false });
  const arranged = arrangeChats(sessions, { folders: ["Work"], chats });
  assert.equal(arranged.recent.length, 21);
  assert.deepEqual(arranged.recent.slice(0, 3).map(({ id }) => id), ["root-0", "root-1", "root-2"]);
  assert.equal(arranged.recent.at(-1).id, "root-24");
  assert.deepEqual(arranged.pinned.map(({ id }) => id), ["pinned"]);
  assert.deepEqual(arranged.folders[0].chats.map(({ id }) => id), ["foldered"]);
});

test("panel drag resizes, hides at the edge, and reopens 2qz", () => {
  assert.deepEqual(panelDrag({ width: 240, hidden: false }, 50, 1000), { width: 290, hidden: false });
  for (let width = 240; width > 0; width -= 8) assert.deepEqual(panelDrag({ width: 240, hidden: false }, width - 240, 1000), { width, hidden: false });
  assert.deepEqual(panelDrag({ width: 240, hidden: false }, -240, 1000), { width: 0, hidden: true });
  assert.deepEqual(panelDrag({ width: 240, hidden: true }, 80, 1000), { width: 80, hidden: false });
});
