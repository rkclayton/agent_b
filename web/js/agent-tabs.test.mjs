import assert from "node:assert/strict";
import test from "node:test";
import { agentTabLayout } from "./agent-tabs.js";

test("one Agent uses the strip without overflow", () => {
  assert.deepEqual(agentTabLayout(1, 240), { visible: 1, hidden: 0 });
});

test("three Agents shrink to the minimum before overflow", () => {
  assert.deepEqual(agentTabLayout(3, 220), { visible: 3, hidden: 0 });
});

test("twelve Agents collapse extras into the end menu", () => {
  assert.deepEqual(agentTabLayout(12, 220), { visible: 2, hidden: 10 });
});

// Item 2mf (a) and (b): a tab's identity is a KIND AND A KEY. These are the rules
// the strip renders from, kept away from the DOM so they can be stated plainly.
import { PLAN_SURFACE, STATIC_SURFACES, canHide, chatSurface, hiddenKinds, isHidden, sameSurface, surfaceForPage, surfaceHref, surfacePage, visibleStaticSurfaces, withHidden } from "./surfaces.js";

test("a chat surface's key is its session; a static surface's key is its own name", () => {
  assert.deepEqual(chatSurface({ id: "s7" }), { kind: "chat", key: "s7" });
  assert.deepEqual(PLAN_SURFACE, { kind: "plan", key: "plan" });
  assert.ok(sameSurface(chatSurface({ id: "s7" }), { kind: "chat", key: "s7" }));
  assert.ok(!sameSurface(chatSurface({ id: "s7" }), { kind: "plan", key: "s7" }));
});

test("a surface has a URL, which is what makes a window possible later", () => {
  assert.equal(surfaceHref(chatSurface({ id: "s7" })), "/chat?session=s7");
  assert.equal(surfaceHref(chatSurface({ id: "" })), "/chat");
  assert.equal(surfaceHref(PLAN_SURFACE, "s7"), "/plan?session=s7");
  assert.equal(surfacePage(PLAN_SURFACE), "plan");
  assert.deepEqual(surfaceForPage("plan"), { kind: "plan", key: "plan" });
  assert.deepEqual(surfaceForPage("chat", "s7"), { kind: "chat", key: "s7" });
  assert.equal(surfaceForPage("settings"), null);
});

test("a hidden surface is hidden, not gone, and only a static surface can hide", () => {
  const shown = { chat: {} };
  assert.deepEqual([...visibleStaticSurfaces(shown)], [...STATIC_SURFACES]);
  const hidden = { chat: { hidden_surfaces: withHidden(shown, PLAN_SURFACE, true) } };
  assert.deepEqual(hidden.chat.hidden_surfaces, ["plan"]);
  assert.ok(isHidden(hidden, PLAN_SURFACE));
  assert.deepEqual(visibleStaticSurfaces(hidden), []);
  // The surface itself is untouched: its URL and its page still answer.
  assert.equal(surfaceHref(PLAN_SURFACE), "/plan");
  // And it comes back where the list puts it, which is last.
  assert.deepEqual(withHidden(hidden, PLAN_SURFACE, false), []);
  // A chat is CLOSED, not hidden.
  assert.ok(canHide(PLAN_SURFACE));
  assert.ok(!canHide(chatSurface({ id: "s7" })));
  // An unknown name in the stored list is ignored rather than trusted.
  assert.deepEqual([...hiddenKinds({ chat: { hidden_surfaces: ["plan", "invented"] } })], ["plan"]);
});
