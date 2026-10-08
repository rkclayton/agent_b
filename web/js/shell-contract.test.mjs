import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { registerMenu } from "./menu-behavior.js";

const shell = await readFile(new URL("./shell.js", import.meta.url), "utf8");
const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const consoleApp = await readFile(new URL("./app.js", import.meta.url), "utf8");
const plan = await readFile(new URL("./plan.js", import.meta.url), "utf8");
const settings = await readFile(new URL("./settings.js", import.meta.url), "utf8");
const tokens = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
const appCSS = await readFile(new URL("../css/app.css", import.meta.url), "utf8");
const chatCSS = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
// Item 2no: one document. The Plan was a page of its own with its own shell slot;
// it is a Settings section now, drawn in this document beside the two panels
// Settings already adopts, so there is one page to hold the contract.
const pages = await Promise.all(["index.html"].map(async (name) => [name, await readFile(new URL(`../${name}`, import.meta.url), "utf8")]));

class MenuRoot extends EventTarget {
  click(target) { for (const type of ["pointerdown", "click"]) { const event = new Event(type); Object.defineProperty(event, "target", { value: target }); this.dispatchEvent(event); } }
  key(key) { const event = new Event("keydown", { cancelable: true }); Object.defineProperty(event, "key", { value: key }); this.dispatchEvent(event); }
}
const menuNode = (parent = null, tag = "div", keep = false) => ({
  parent, tag, keep, hidden: false, isConnected: true,
  contains(target) { for (let at = target; at; at = at.parent) if (at === this) return true; return false; },
  closest(selector) { if (selector === "[data-menu-reveal]") return this.keep ? this : this.parent?.closest?.(selector); if (selector === "button, a") return ["button", "a"].includes(this.tag) ? this : this.parent?.closest?.(selector); return null; },
});

test("one registered menu owns outside click, action click, Escape, another menu, entries and redraws", () => {
  const root = new MenuRoot(), anchor = menuNode(), menu = menuNode(), empty = menuNode(), control = menuNode();
  menu.hidden = true;
  const first = registerMenu(menu, { anchor, root });
  first.open(); root.click(empty); assert.equal(menu.hidden, true);
  let acted = 0; first.open(); acted++; root.click(control); assert.equal(menu.hidden, true); assert.equal(acted, 1);
  first.open(); root.key("Escape"); assert.equal(menu.hidden, true);
  const otherMenu = menuNode(); otherMenu.hidden = true; const other = registerMenu(otherMenu, { anchor: menuNode(), root });
  first.open(); other.open(); assert.equal(menu.hidden, true); assert.equal(otherMenu.hidden, false);
  first.open(); root.click(menuNode(menu, "button")); assert.equal(menu.hidden, true);
  first.open(); root.click(menuNode(menu, "button", true)); assert.equal(menu.hidden, false);
  const redraw = () => acted++; redraw(); assert.equal(menu.hidden, false); assert.equal(acted, 2);
});

test("every product menu registers the one shared behavior", () => {
  assert.match(shell, /registerMenu\(newChatMenu/);
  assert.match(shell, /registerMenu\(connectionMenu/);
  assert.match(shell, /registerMenu\(menu, \{ anchor: more/);
  assert.match(chat, /registerMenu\(attachMenu/);
  assert.match(settings, /registerMenu\(popover/);
  assert.doesNotMatch(shell, /openMenuSession|session\.id === openMenuSession/);
});

test("shared shell slot order is identical on the chat and Plan", () => {
  for (const [name, html] of pages) {
    assert.match(html, new RegExp(`id="app-shell"[^>]+data-page="${name === "index.html" ? "chat" : "plan"}"`));
    assert.doesNotMatch(html, /id="(?:shell-stop|shell-state|shell-operator-status)"/);
  }
  assert.match(shell, /root\.append\(left, right\)/);
  // Item 2mf (c): the one-entry pages nav is gone and Plan is a tab in the strip
  // on the LEFT, so the right slot is one element shorter and the plan entry it
  // held is not there to assert any more.
  // Item 2px (f): the chat's connection lamp sits before the heading it belongs to.
  assert.match(shell, /right\.append\(sessionActivity, sessionLamp, sessionHeading, connectionMenu, settings, windowControls\)/);
  assert.doesNotMatch(shell, /shell-operator-status|right\.append\(stop/);
  assert.doesNotMatch(shell, /\[\["plan", "\/plan"\]\]/);
  // Item 2ni: and the tab is gone too. "i decided i think i want it under settings,
  // as its own top level item" — so the strip draws chats and nothing else, and the
  // static-surface pass that drew the Plan is not here to assert any more.
  assert.doesNotMatch(shell, /renderStaticSurfaces/);
  assert.doesNotMatch(shell, /\["chat", "Chat", "\/chat"\]|\["console", "Console", "\/"\]/);
  // Item 2gk: the page it used to flip to is gone, so the flip is gone with it.
  assert.doesNotMatch(shell, /Console/);
  assert.doesNotMatch(shell, /rememberedAgentSide|rememberAgentSide\(|agentb\.side\./);
});

test("the menu keeps the plan choice for d while plus always creates an ordinary chat", () => {
  assert.match(shell, /const hasD = !!String\(configured\?\.d \|\| ""\)\.trim\(\)/);
  assert.match(shell, /agent_d · \$\{name\} — plan/);
  assert.match(shell, /createChat\("agent_d", configured\)/);
  assert.match(shell, /newChatButton\.onclick = \(\) => void createChat\("agent_b"\)/);
  assert.doesNotMatch(shell, /planRepoEditor|showPlanMenu|plan_id/);
});

// Item 2mf: Plan is a TAB now, not a one-entry nav. The chip is the same file at
// the same drawn size and the accessible name is still there; what moved is where
// it lives. The pages nav is gone and this asserts that too.
//
// Item 2ni: it moved again, and for the last time — into the Settings nav as its own
// top-level entry. Same three files, same 12px, same class; the strip no longer
// draws it at all, and the entry's name is the accessible name.
test("Plan is a compact accessible chip icon, in the Settings nav", () => {
  assert.match(settings, /class="shell-page-chip"/);
  assert.match(settings, /plan-mark-nav\.png 1x, [^"]*@2x\.png 2x, [^"]*@3x\.png 3x/);
  assert.match(settings, /\["plan", "Plan"\]/);
  assert.doesNotMatch(shell, /shell-page-chip/);
  assert.doesNotMatch(shell, /node\("nav", "shell-pages"\)/);
  assert.doesNotMatch(shell, /\[\["plan", "\/plan"\]\]/);
  assert.match(shell, /node\("button", "shell-settings"\)/);
  assert.doesNotMatch(shell, /link\.href|settings\.href/);
});

test("compact window controls continue the top strip", () => {
  assert.match(shell, /\["minimize", "maximize", "close"\]/);
  assert.match(tokens, /\.shell-window-controls\{[^}]*grid-template-columns:repeat\(3,28px\)/);
  assert.match(tokens, /\.shell-window-control\{[^}]*width:28px/);
  assert.match(tokens, /\.shell-window-control-glyph\{[^}]*width:9px;height:9px/);
  assert.match(shell, /sessionHeading\.textContent = message/);
  assert.match(shell, /sessionHeading\.classList\.add\("alarm"\)/);
});

test("chat list keeps compact controls and transient chrome", () => {
  assert.match(shell, /iconButton\("menu", "", "chat-list-primary-action chat-list-menu-button"\)/);
  assert.match(shell, /iconButton\("plus", "New chat", "chat-list-primary-action chat-list-new"\)/);
  assert.match(shell, /label\.textContent = "New"[\s\S]*button\("Chat"[\s\S]*button\("Folder"[\s\S]*button\("Settings"/);
  assert.doesNotMatch(shell, /rootHeading\.textContent = "Chats"/);
  assert.match(shell, /classList\.add\("menu-open"\)/);
  assert.match(shell, /shell-session-activity/);
  assert.match(chatCSS, /chat-list-row:hover \.chat-list-more/);
  assert.match(chatCSS, /scrollbar-width:thin/);
  assert.doesNotMatch(shell, /button\("New chat", "New chat"/);
});

test("Stop follows the selected chat from each page-local lower control", () => {
  assert.match(chat, /api\("\/api\/stop", \{ session_id: session\.id \}\)/);
  assert.match(consoleApp, /api\("\/api\/stop",\{session_id:id\}\)/);
  // Item 2fc: the Plan page shows plans, not a chat; the planning chat is its own tab.
  assert.doesNotMatch(plan, /mountChat/);
  assert.doesNotMatch(chat+consoleApp+plan, /all:\s*true/);
});

test("top-right connection label is the b-role model switcher", () => {
  assert.match(shell, /button\("", "Switch model", "shell-session-title"\)/);
  assert.match(shell, /for \(const connection of store\.connections \|\| store\.config\.connections \|\| \[\]\)/);
  assert.match(shell, /connection\.label \|\| connection\.id[\s\S]*new URL\(host\)\.host[\s\S]*connectionState\(connection, session\)/);
  assert.match(shell, /if \(isRunning\(current\)\)[\s\S]*stop the run first/);
  assert.match(shell, /api\(`\/api\/agents\/\$\{encodeURIComponent\(agentID\)\}\/connection`, \{ action: "set", connection_id: connection\.id \}\)/);
  assert.match(shell, /const selected = \{ \.\.\.store\.selection \}[\s\S]*setSelection\(selected\.agent_id \|\| "agent_b", selected\.session_id \|\| ""\)/);
  assert.match(shell, /session\.runnable === false \? \(notRunnableReason \|\| "Chat is not runnable"\) : sessionTitle\(session\)/);
  assert.match(shell, /"No chat selected"/);
  assert.match(shell, /\/api\/header-state/);
  assert.doesNotMatch(shell, /setProperty\(sessionHeading, "hidden", !session\)/);
  assert.match(settings, /context unknown — enter the size/);
  assert.match(settings, /nctx > caps\.n_ctx/);
});

test("all shell motion is zero duration under reduced motion", () => {
  assert.match(tokens, /prefers-reduced-motion:reduce[\s\S]*\.app-shell[\s\S]*animation-duration:0ms!important/);
});

test("the whole-app robot is a non-control in the drag strip and shares the composer rules", () => {
  assert.match(shell, /node\("span", "shell-app-robot chat-run-robot idle"\)/);
  assert.match(shell, /left\.append\(appRobot\)/);
  assert.doesNotMatch(shell, /appRobot\.(?:onclick|onpointer|tabIndex)/);
  assert.match(tokens, /\.chat-run-robot\{[^}]*animation:chat-run-robot/);
  assert.doesNotMatch(chatCSS, /\.chat-run-robot\s*\{/);
});

// Item 2gh (v1.1.2/W5): the tab menu, measured and made to feel right. These
// pin the four defects the measurement found, so none can come back quietly.
// Item 2ge (v1.1.2/W6), glyph replaced by 2he (v1.3.0/W2): the Plan toggle is
// the processor chip in the operator's accent, and that accent has exactly one
// use in the product.
test("the Plan toggle is the chip in the accent, and the accent is used once", async () => {
  assert.match(settings, /class="shell-page-chip"/);
  assert.doesNotMatch(shell, /M9 4\.5A3\.5 3\.5 0 0 0 5\.5 8/); // the mark he did not recognise
  assert.doesNotMatch(shell, /shell-page-brain/); // 2he: the traced brain is gone
  assert.match(tokens, /--accent-plan:#5AC8FA;/);
  // Item 2le: the chip is the operator's own artwork now, so it carries its own
  // colour and needs no stroke. The accent keeps exactly one use, on the Plan
  // header the full-quality version of that same artwork heads.
  assert.match(tokens, /\.shell-page-chip\{display:block;width:12px;height:12px;opacity:\.6\}/);
  // rel-1.25.0 card 7: the chip's hover and selected rules hang off the entry that
  // carries it, because .shell-page renders nothing. The chip itself, its size and
  // its opacity ramp are unchanged through both moves.
  // Item 2ni: the ramp follows the entry into the Settings nav. Same opacities.
  assert.match(tokens, /\.settings-nav button:hover \.shell-page-chip\{opacity:\.8\}/);
  assert.match(tokens, /\.settings-nav button\.selected \.shell-page-chip\{opacity:1\}/);
  assert.doesNotMatch(tokens, /\.shell-page[,.:{]/);
  // One element, and one only: every var(--accent-plan) in every stylesheet
  // must be a .shell-page-chip rule.
  const styles = await Promise.all(["tokens.css", "app.css", "chat.css", "plan.css", "setup.css"]
    .map(async (name) => [name, await readFile(new URL(`../css/${name}`, import.meta.url), "utf8").catch(() => "")]));
  const uses = [];
  for (const [name, css] of styles) {
    for (const rule of css.split("}")) if (rule.includes("var(--accent-plan)")) uses.push(`${name}: ${rule.split("*/").pop().trim()}`);
  }
  assert.equal(uses.length, 1, uses.join(" | "));
  assert.match(uses[0], /plan-intro/);
  // The palette rule records it as the operator's exception, not as a seventh
  // colour quietly added to the list.
  const design = await readFile(new URL("../DESIGN.md", import.meta.url), "utf8");
  assert.match(design, /The one exception, stated by the operator \(item 2ge/);
  assert.match(design, /Accent-plan `#5AC8FA`/);
});

// Item 2he (v1.3.0/W2): the toggle renders web/assets/plan-chip.svg VERBATIM.
// The operator picked this glyph from three candidates and asked for it as
// drawn, so the test compares the shapes in shell.js against the asset itself
// rather than against a copy of them written here -- a redraw, a simplification
// or a "tidied" path all fail, and the asset stays the single source.
// Item 2le (v1.14.0): the operator supplied his own artwork — "i want this to be
// the new plan icon … maintain the color of the icon itself" — so the chip is that
// prepared asset, and the drawn 24-grid SVG it replaces is gone from shell.js.
test("the Plan chip is the operator's prepared artwork, not a drawing", async () => {
  // Item 2ni: read from settings.js, which is where the entry the chip belongs to
  // now lives. The asset, the size set and the class are unchanged.
  assert.match(settings, /src="\/static\/assets\/plan-mark-nav\.png"/);
  assert.match(settings, /class="shell-page-chip"/);
  // The drawing it replaces is not left behind beside it.
  assert.doesNotMatch(settings, /<rect x="7" y="7" width="10" height="10" rx="2"\/>/);
  assert.doesNotMatch(shell, /stroke-width="2"/);
  // Item 2lm (b): the nav mark is a SIZE SET now. The strip draws a 12px box and
  // the product runs at more than one device pixel ratio -- rel-1.16.0/W0
  // measured the operator's own display at 1.75x, where that box is 21 physical
  // pixels while every screenshot captures it at 12. One asset cannot be sharp at
  // all of them, so the browser picks.
  assert.match(settings, /srcset="[^"]*plan-mark-nav\.png 1x[^"]*plan-mark-nav@2x\.png 2x[^"]*plan-mark-nav@3x\.png 3x"/);
  // Every size ships, and each is a real PNG with an alpha channel: the background
  // is removed, not repainted, so the mark sits on whatever is behind it.
  for (const [name, expected] of [["plan-mark-nav.png", 12], ["plan-mark-nav@2x.png", 24], ["plan-mark-nav@3x.png", 36], ["plan-mark.png", 128]]) {
    const png = await readFile(new URL(`../assets/${name}`, import.meta.url));
    assert.equal(png.subarray(1, 4).toString("ascii"), "PNG", `${name} is not a PNG`);
    assert.equal(png.readUInt32BE(16), expected, `${name} is not ${expected}px wide`);
    assert.equal(png.readUInt8(25), 6, `${name} has no alpha channel`);
  }
  const tokensCss = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
  const chipRule = (tokensCss.split("}").find((rule) => rule.includes(".shell-page-chip{")) ?? "")
    .replace(/\/\*[\s\S]*?\*\//g, "").split(".shell-page-chip{").pop();
  assert.doesNotMatch(chipRule, /stroke/, "the artwork carries its own colour; no stroke is applied to it");
  // The operator explicitly halved the displayed box; item 2lm (c) says the drawn
  // size is not changed to make the artwork work, so it is still 12.
  assert.match(chipRule, /width:12px;height:12px/);
});

// Item 2lm @keep: the full-detail Plan page header is exactly what v1.15.0 ships.
// Only the nav variant was prepared again.
test("the full-detail Plan mark is untouched", async () => {
  const png = await readFile(new URL("../assets/plan-mark.png", import.meta.url));
  assert.equal(png.readUInt32BE(16), 128);
  assert.equal(png.readUInt32BE(20), 128);
  const plan = await readFile(new URL("../index.html", import.meta.url), "utf8");
  assert.match(plan, /plan-mark\.png/, "the Plan section no longer heads with the full-detail mark");
});

// Item 2gk (v1.3.0/W2): the readout left, and the marker that drew
// tofu is gone. Operator, 2026-09-22, with a screenshot of the line sitting as
// a header above the transcript: "i want this moved".
test("the attachment control replaces the redundant per-chat readout", async () => {
  const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
  const actions = html.slice(html.indexOf('class="chat-input-actions"'), html.indexOf("</span>", html.indexOf('id="chat-mic"')));
  assert.ok(actions.indexOf('id="chat-attach"') < actions.indexOf('id="chat-mic"'), "the attachment control sits directly above the microphone");
  assert.ok(!html.includes('id="chat-readout"'), "the duplicate token readout is gone");
  // It must sit in the composer footer, not between transcript and composer.
  const log = html.indexOf('id="chat-log"');
  const composer = html.indexOf('id="chat-composer"');
  const attachment = html.indexOf('id="chat-attach"');
  assert.ok(attachment > composer, "the attachment control must live inside the composer footer");
  assert.ok(log < composer, "the transcript still precedes the composer");

  const chatCss = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
  assert.match(chatCss, /\.chat-attach-wrap \{ position: relative; display:block; width:24px; height:24px; \}/);
  // No control characters anywhere in the stylesheet: the marker was a raw
  // 0x15 byte, which is how it reached the operator's screen as tofu.
  const control = [...chatCss].filter((ch) => ch.charCodeAt(0) < 32 && !"\r\n\t".includes(ch));
  assert.equal(control.length, 0, `stylesheet holds ${control.length} control character(s)`);
  assert.doesNotMatch(chatCss, /\.chat-readout > summary::before/, "the removed readout cannot reintroduce tofu");
});

// Item 2ms: the two rules that kept a closed chat on screen, asserted where they
// live. The sighting was reproduced against a copy of the operator's own 34 restored
// chats: 33 closed, and the one that was not is role `c` - a WORKER, which the strip
// refuses to draw and which the pane's fallback happily bound to.
test("Delete says what it does and works on a running chat (2py)", () => {
  assert.match(shell, /const deleteConfirmText = "Delete this chat permanently\? Memory notes it made are kept\.";/);
  assert.match(shell, /button\(labels\[3\], labels\[3\], "chat-list-menu-action alarm"\)/);
  assert.match(shell, /remove\.onclick = \(\) => \{ menuControllers\.get\(menu\)\?\.close\(\); void deleteChat\(session\); \}/);
  assert.doesNotMatch(shell, /Stop it before deleting the chat/);
});
