import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const chat = readFileSync(new URL("./chat.js", import.meta.url), "utf8");
const shell = readFileSync(new URL("./shell.js", import.meta.url), "utf8");
const robot = readFileSync(new URL("./agent-robot.js", import.meta.url), "utf8");
const chatStyles = readFileSync(new URL("../css/chat.css", import.meta.url), "utf8");
const sharedStyles = readFileSync(new URL("../css/tokens.css", import.meta.url), "utf8");
const styles = `${chatStyles}\n${sharedStyles}`;

test("one shared drawing replaces the old image in both places 2tj", () => {
  assert.match(shell, /createAgentRobot\(document, \{ place: "shell" \}\)/);
  assert.match(chat, /createAgentRobot\(document, \{ place: "chat" \}\)/);
  assert.equal([...robot.matchAll(/<svg /g)].length, 1);
  assert.doesNotMatch(`${shell}\n${chat}`, /agent\.svg|chat-run-robot-eyes/);
});

test("the running robot is live state, present only while a run is live", () => {
  // Bound to the live activity line, which is empty unless run.status is running.
  assert.match(chat, /const running = !!activity;/);
  // rel-1.42.0 (2o2): the robot node is built once and kept; it takes its class
  // only while a run is live and is hidden on every other condition.
  assert.match(chat, /if \(running && !promptOnly\) \{[\s\S]{0,400}updateAgentRobot/);
  assert.match(chat, /setProperty\(composerRobot, "hidden", !running \|\| promptOnly\)/);
});

test("the running robot takes the strip's existing height and adds no chrome", () => {
  assert.match(styles, /\.chat-live-robot\{[^}]*width:16px[^}]*height:16px/);
  const notice = styles.match(/\.chat-notice \{([^}]+)\}/);
  assert.ok(notice, "no .chat-notice rule");
  // 12px glyph inside the strip's existing 16px line box: no reflow, no growth.
  assert.match(notice[1], /min-height: 16px/);
  assert.match(notice[1], /line-height: 16px/);
  assert.match(notice[1], /display: flex/);
});

test("the running robot uses palette tokens only and no glow", () => {
  assert.match(styles, /\.agent-robot\.running\{color:var\(--signal\)\}/);
  assert.match(styles, /\.agent-robot\.waiting,\.agent-robot\.offline\s*\{color:var\(--alarm\)\}/);
  const block = styles.slice(styles.indexOf(".agent-robot{"), styles.indexOf("@media (prefers-reduced-motion:reduce)"));
  assert.doesNotMatch(block, /#[0-9a-fA-F]{3,6}/, "no raw hex on the run robot");
  assert.doesNotMatch(block, /box-shadow:[^;]*rgba/, "no glow");
  assert.doesNotMatch(block, /scaleX\(-1\)|translateY\(-1px\)/, "no flip or hop");
});

test("its eyes carry the same state colours the chat list shows", () => {
  assert.match(styles, /\.chat-list-state\.running \{ background: var\(--signal\); \}/);
  assert.match(styles, /\.chat-list-state\.waiting,\.chat-list-state\.offline \{ background: var\(--alarm\); \}/);
  assert.match(styles, /\.agent-robot-eye\s*\{[^}]*fill:currentColor/);
  // The class the renderer picks mirrors chatState's own vocabulary.
  assert.match(chat, /model_unreachable \? "offline" :[\s\S]{0,120}"waiting" : "running"/);
});

test("reduced motion freezes the robot without removing it", () => {
  const reduced = styles.slice(styles.indexOf("@media (prefers-reduced-motion: reduce)"));
  assert.match(reduced, /animation-duration: 0ms !important/);
  // The robot lives inside .chat-page and is not .app-shell, so the blanket rule
  // reaches it. Nothing hides it: the frame stays, the motion stops.
  assert.match(reduced, /\.agent-robot \*\{animation:none!important\}/);
});

test("the two placements have different blink rhythms and only transform or opacity moves 2tj", () => {
  assert.match(styles, /\.shell-app-robot\{[^}]*--agent-robot-blink:5\.3s/);
  assert.match(styles, /\.chat-live-robot\{[^}]*--agent-robot-blink:4\.1s/);
  for (const body of styles.matchAll(/@keyframes agent-robot-[^{]+\{([^@]+?)(?=@keyframes|@media|$)/gs)) {
    assert.doesNotMatch(body[1], /(?:left|right|top|bottom|width|height|margin|padding):/);
  }
});

test("the live line's own text is unchanged beside the glyph", () => {
  assert.match(chat, /composerText\.className = "chat-notice-text";/);
  assert.match(chat, /setText\(composerText, message\);/);
});
