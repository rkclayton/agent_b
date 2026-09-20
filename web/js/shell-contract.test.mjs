import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const shell = await readFile(new URL("./shell.js", import.meta.url), "utf8");
const chat = await readFile(new URL("./chat.js", import.meta.url), "utf8");
const consoleApp = await readFile(new URL("./app.js", import.meta.url), "utf8");
const plan = await readFile(new URL("./plan.js", import.meta.url), "utf8");
const tokens = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
const appCSS = await readFile(new URL("../css/app.css", import.meta.url), "utf8");
const chatCSS = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
const pages = await Promise.all(["index.html", "plan.html"].map(async (name) => [name, await readFile(new URL(`../${name}`, import.meta.url), "utf8")]));

test("shared shell slot order is identical on the chat and Plan", () => {
  for (const [name, html] of pages) {
    assert.match(html, new RegExp(`id="app-shell"[^>]+data-page="${name === "index.html" ? "chat" : "plan"}"`));
    assert.doesNotMatch(html, /id="(?:shell-stop|shell-state|shell-operator-status)"/);
  }
  assert.match(shell, /root\.append\(left, right\)/);
  assert.match(shell, /right\.append\(sessionHeading, pages, settings\)/);
  assert.doesNotMatch(shell, /shell-operator-status|right\.append\(stop/);
  assert.match(shell, /\[\["plan", "\/plan"\]\]/);
  assert.doesNotMatch(shell, /\["chat", "Chat", "\/chat"\]|\["console", "Console", "\/"\]/);
  // Item 2gk: the page it used to flip to is gone, so the flip is gone with it.
  assert.doesNotMatch(shell, /Console/);
  assert.doesNotMatch(shell, /rememberedAgentSide|rememberAgentSide\(|agentb\.side\./);
});

test("each open chat gets an agent tab whose robot eyes expose that chat state", () => {
  // Item 2gn: the strip also carries the selected CLOSED chat — the one the
  // operator is looking at — so the filter admits it and spans lines.
  assert.match(shell, /const open = Object\.values\(store\.sessions\)[\s\S]{0,40}\.filter/);
  assert.match(shell, /!session\.closed \|\| session\.id === store\.selection\.session_id/);
  assert.match(shell, /for \(const session of rendered\)/);
  assert.match(shell, /wrap\.dataset\.session = session\.id/);
  assert.match(shell, /return "waiting"[\s\S]*return "running"[\s\S]*return "idle"/);
  assert.doesNotMatch(shell, /class="agent-state/);
  assert.match(shell, /agent-tab-robot-\$\{robot\} \$\{glyphState\}/);
  assert.match(shell, /<span class="agent-tab-eyes"><\/span>/);
  assert.match(tokens, /\.agent-tab-robot\.running\{color:var\(--trace\)\}/);
  assert.match(tokens, /\.agent-tab-robot\.offline\{color:var\(--alarm\)\}/);
  assert.match(tokens, /\.agent-tab-robot\.waiting\{color:var\(--alarm\)\}/);
  assert.match(shell, /session\?\.model_unreachable\) return "offline"/);
  assert.match(shell, /\(store\.servers \|\| \[\]\)\.find/);
  assert.match(shell, /left\.append\(newChatButton, newChatMenu, tabs\)/);
  assert.match(shell, /newChatButton\.onclick = \(\) => hasD \? showRoleMenu/);
  assert.doesNotMatch(shell, /wrap\.append\([^\n]*(?:agent-tab-new|newChatButton)/);
  assert.match(shell, /newChatButton\.disabled = store\.replay \|\| !\(store\.config\.agents \|\| \[\]\)\.length/);
  assert.match(tokens, /--agent-tab-width:118px/);
  assert.match(tokens, /\.agent-tab-wrap\{[^}]*flex:0 0 var\(--agent-tab-width\)/);
  assert.match(tokens, /\.agent-tab\{[^}]*flex:1 1 auto;[^}]*min-width:69px/);
  // Left click only selects; there is one side to be on.
  assert.match(shell, /if \(options\.switchView\) options\.switchView\(next, navigation\)/);
  assert.doesNotMatch(shell, /flip\.label|flip\.open/);
  assert.match(shell, /tab\.onclick = \(\) => \{[\s\S]{0,400}setSelection\(agentID, session\.id\);/);
  assert.doesNotMatch(shell, /tab\.onclick = \(\) => \{[\s\S]{0,400}switchView/);
  // The close mark overlays the tab rather than extending the strip.
  assert.match(tokens, /\.agent-tab-close\{position:absolute/);
  assert.match(tokens, /\.agent-tab-wrap \.agent-tab\{padding-right:18px\}/);
  assert.match(tokens, /\.agent-tab\.side-chat\{color:var\(--ink\)\}/);
  assert.match(tokens, /\.agent-tab\.side-console\{color:var\(--ink\)\}/);
  assert.match(tokens, /\.agent-tab-wrap\.selected\.side-console\{background:rgba\(216,221,227,.16\)\}/);
  assert.match(shell, /button\("", chatName/);
  assert.match(shell, /<span>\$\{escapeHTML\(agentID\)\}<\/span>/);
  assert.match(shell, /button\("×", `Close \$\{chatName\}`/);
  assert.match(shell, /const agentID = `agent_\$\{session\?\.role === "d" \? "d" : "b"\}`/);
  assert.match(shell, /document\.title = session \? sessionTitle\(session\) : "Agent_b"/);
});

test("plus adds a two-line d choice only for an assigned d profile", () => {
  assert.match(shell, /const hasD = !!String\(configured\?\.d \|\| ""\)\.trim\(\)/);
  assert.match(shell, /agent_b · \$\{name\} — chat/);
  assert.match(shell, /agent_d · \$\{name\} — plan/);
  assert.match(shell, /createChat\("agent_d", configured\)/);
  assert.doesNotMatch(shell, /planRepoEditor|showPlanMenu|plan_id/);
});

test("Plan is a compact accessible brain icon", () => {
  assert.match(shell, /class="shell-page-icon shell-page-brain"/);
  assert.match(shell, /setAttribute\("aria-label", "plan"\)/);
  assert.match(shell, /link\.title = "plan"/);
  assert.doesNotMatch(shell, /\[\["plan", "Plan", "\/plan"\]\]/);
});

test("agent menu is the counted open and closed chat history with glyph controls", () => {
  assert.match(shell, /sessionsFor\(agentID, true\)/);
  assert.match(shell, /oncontextmenu/);
  assert.match(shell, /button\("Open"/);
  assert.match(shell, /\/reopen`/);
  assert.match(shell, /button\("×"/);
  assert.match(shell, /button\("🗑"/);
  assert.match(shell, /button\("Rename"/);
  assert.match(shell, /agent-chat-rename-form/);
  assert.match(shell, /\{ label \}/);
  assert.match(shell, /openCount[\s\S]*closed/);
  assert.match(shell, /confirm: false/);
  assert.match(shell, /delete-confirm/);
  assert.match(shell, /confirm: true, drop_memory: dropMemory\.checked/);
  assert.doesNotMatch(shell, /window\.confirm\([^)]*Delete/);
  assert.doesNotMatch(shell, /window\.confirm\([^)]*closeConfirmText|closeConfirmText/);
  // Item 2gh: the tab menu opens AT THE POINTER, so the reveal carries the
  // event's coordinates. It used to be revealMenu(menu, tab), which placed it
  // at the tab and put its left edge 41 px from the pointer (W5's measurement).
  assert.match(shell, /revealMenu\(menu, tab, \{ x: event\.clientX, y: event\.clientY \}\)/);
  assert.match(tokens, /\.shell-menu\{position:fixed/);
  assert.match(tokens, /max-height:calc\(100vh - 50px\);overflow-x:hidden;overflow-y:auto/);
});

test("unbounded chat tabs scroll only inside the tab strip", () => {
  assert.match(tokens, /\.agent-tabs\{[^}]*overflow-x:auto;overflow-y:hidden/);
  assert.doesNotMatch(shell, /agentTabLayout|agent-overflow/);
  assert.doesNotMatch(appCSS + chatCSS, /overflow-x:\s*(?:auto|scroll)/);
});

test("Stop follows the selected chat from each page-local lower control", () => {
  assert.match(chat, /api\("\/api\/stop", \{ session_id: session\.id \}\)/);
  assert.match(consoleApp, /api\("\/api\/stop",\{session_id:id\}\)/);
  // Item 2fc: the Plan page shows plans, not a chat; the planning chat is its own tab.
  assert.doesNotMatch(plan, /mountChat/);
  assert.doesNotMatch(chat+consoleApp+plan, /all:\s*true/);
});

test("top bar gives fixed readable tabs a scoped horizontal scroll lane", () => {
  assert.match(tokens, /\.shell-left\{overflow:hidden/);
  assert.match(tokens, /\.agent-tabs\{[^}]*overflow-x:auto/);
  assert.match(tokens, /\.agent-tab-wrap\{[^}]*min-width:var\(--agent-tab-width\)/);
  assert.match(tokens, /\.agent-tab[^\n]*white-space:nowrap/);
  assert.doesNotMatch(shell, /shell-selection/);
});

test("all shell motion is zero duration under reduced motion", () => {
  assert.match(tokens, /prefers-reduced-motion:reduce[\s\S]*\.app-shell[\s\S]*animation-duration:0ms!important/);
});

// Item 2gh (v1.1.2/W5): the tab menu, measured and made to feel right. These
// pin the four defects the measurement found, so none can come back quietly.
test("the tab menu opens at the pointer, dismisses three ways and takes the arrow keys", () => {
  // Opens at the pointer, and still clamped inside the window.
  assert.match(shell, /function revealMenu\(menu, anchor, point = null\)/);
  assert.match(shell, /const left = point \? point\.x : anchorRect\.left;/);
  assert.match(shell, /const top = point \? point\.y : anchorRect\.bottom;/);
  assert.match(shell, /Math\.max\(8, Math\.min\(left, innerWidth - menuRect\.width - 8\)\)/);
  assert.match(shell, /Math\.max\(8, Math\.min\(top, innerHeight - menuRect\.height - 8\)\)/);
  // A second right-click on the same tab dismisses it.
  assert.match(shell, /if \(!menu\.hidden\) \{ menu\.hidden = true; return; \}/);
  // Escape dismisses, and the arrows move through the entries without the
  // menu stealing focus when it opens.
  assert.match(shell, /if \(event\.key !== "Escape" && event\.key !== "ArrowDown" && event\.key !== "ArrowUp"\) return;/);
  assert.match(shell, /if \(event\.key === "Escape"\) \{\s*\n\s*menu\.hidden = true;/);
  assert.match(shell, /rows\[next\]\.focus\(\);/);
  // The entries themselves are unchanged: none added, none removed.
  assert.doesNotMatch(shell, /menu\.appendChild\(document\.createElement\("hr"\)\)/);
});

// Item 2ge (v1.1.2/W6): the Plan toggle is a side-profile brain in the
// operator's accent, and that accent has exactly one use in the product.
test("the Plan toggle is a brain in the accent, and the accent is used once", async () => {
  assert.match(shell, /class="shell-page-icon shell-page-brain"/);
  assert.doesNotMatch(shell, /M9 4\.5A3\.5 3\.5 0 0 0 5\.5 8/); // the mark he did not recognise
  assert.match(tokens, /--accent-plan:#5AC8FA;/);
  assert.match(tokens, /\.shell-page-brain\{stroke:var\(--accent-plan\);stroke-width:1\.4;opacity:\.45\}/);
  assert.match(tokens, /\.shell-page:hover \.shell-page-brain\{opacity:\.8\}/);
  assert.match(tokens, /\.shell-page\.selected \.shell-page-brain\{opacity:1\}/);
  // One element, and one only: every var(--accent-plan) in every stylesheet
  // must be a .shell-page-brain rule.
  const styles = await Promise.all(["tokens.css", "app.css", "chat.css", "plan.css", "setup.css"]
    .map(async (name) => [name, await readFile(new URL(`../css/${name}`, import.meta.url), "utf8").catch(() => "")]));
  const uses = [];
  for (const [name, css] of styles) {
    for (const rule of css.split("}")) if (rule.includes("var(--accent-plan)")) uses.push(`${name}: ${rule.split("*/").pop().trim()}`);
  }
  assert.equal(uses.length, 1, uses.join(" | "));
  assert.match(uses[0], /shell-page-brain/);
  // The palette rule records it as the operator's exception, not as a seventh
  // colour quietly added to the list.
  const design = await readFile(new URL("../DESIGN.md", import.meta.url), "utf8");
  assert.match(design, /The one exception, stated by the operator \(item 2ge/);
  assert.match(design, /Accent-plan `#5AC8FA`/);
});
