import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const chat = readFileSync(new URL("./chat.js", import.meta.url), "utf8");
const styles = readFileSync(new URL("../css/chat.css", import.meta.url), "utf8");

test("the running robot is live state, present only while a run is live", () => {
  // Bound to the live activity line, which is empty unless run.status is running.
  assert.match(chat, /const running = !!activity;/);
  assert.match(chat, /if \(running\) \{[\s\S]{0,400}chat-run-robot/);
  // Nothing renders it on any other condition: the class name appears only in
  // the guarded block (once on the element, once on its eyes).
  assert.equal([...chat.matchAll(/chat-run-robot/g)].length, 2);
  const guarded = chat.slice(chat.indexOf("if (running) {"), chat.indexOf("const text = document.createElement"));
  assert.equal([...guarded.matchAll(/chat-run-robot/g)].length, 2);
});

test("the running robot takes the strip's existing height and adds no chrome", () => {
  const rule = styles.match(/\.chat-run-robot \{([^}]+)\}/);
  assert.ok(rule, "no .chat-run-robot rule");
  assert.match(rule[1], /width: 12px/);
  assert.match(rule[1], /height: 12px/);
  const notice = styles.match(/\.chat-notice \{([^}]+)\}/);
  assert.ok(notice, "no .chat-notice rule");
  // 12px glyph inside the strip's existing 16px line box: no reflow, no growth.
  assert.match(notice[1], /min-height: 16px/);
  assert.match(notice[1], /line-height: 16px/);
  assert.match(notice[1], /display: flex/);
});

test("the running robot uses palette tokens only and no glow", () => {
  const rule = styles.match(/\.chat-run-robot \{([^}]+)\}/)[1];
  assert.match(rule, /color: var\(--trace\)/);
  assert.match(styles, /\.chat-run-robot\.waiting \{ color: var\(--alarm\); \}/);
  assert.match(styles, /\.chat-run-robot\.offline \{ color: var\(--alarm\); \}/);
  const block = styles.slice(styles.indexOf(".chat-run-robot {"), styles.indexOf("@keyframes chat-run-robot") + 200);
  assert.doesNotMatch(block, /#[0-9a-fA-F]{3,6}/, "no raw hex on the run robot");
  assert.doesNotMatch(block, /box-shadow:[^;]*rgba/, "no glow");
  assert.doesNotMatch(block, /ease|cubic-bezier/, "no easing");
});

test("its eyes carry the same state colours the tab robot shows", () => {
  const tokens = readFileSync(new URL("../css/tokens.css", import.meta.url), "utf8");
  assert.match(tokens, /\.agent-tab-robot\.running\{color:var\(--trace\)\}/);
  assert.match(tokens, /\.agent-tab-robot\.waiting\{color:var\(--alarm\)\}/);
  assert.match(styles, /\.chat-run-robot-eyes \{[^}]*background: currentColor/);
  // The class the renderer picks mirrors chatState's own vocabulary.
  assert.match(chat, /model_unreachable \? "offline" :[\s\S]{0,120}"waiting" : "running"/);
});

test("reduced motion freezes the robot without removing it", () => {
  const reduced = styles.slice(styles.indexOf("@media (prefers-reduced-motion: reduce)"));
  assert.match(reduced, /animation-duration: 0ms !important/);
  // The robot lives inside .chat-page and is not .app-shell, so the blanket rule
  // reaches it. Nothing hides it: the frame stays, the motion stops.
  assert.doesNotMatch(reduced, /\.chat-run-robot[^{]*\{[^}]*display:\s*none/);
});

test("the live line's own text is unchanged beside the glyph", () => {
  assert.match(chat, /text\.className = "chat-notice-text";/);
  assert.match(chat, /text\.textContent = message;/);
});
