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
  assert.match(shell, /right\.append\(sessionHeading, connectionMenu, pages, settings, windowControls\)/);
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
  assert.match(shell, /\(store\.connections \|\| \[\]\)\.find/);
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
  // Item 2gk removed the second side; item 2go put the chat's NAME on the tab,
  // with a fixed width and an ellipsis for a long one.
  assert.match(tokens, /\.agent-tab-name\{[^}]*text-overflow:ellipsis/);
  assert.match(shell, /class="agent-tab-name"/);
  assert.match(tokens, /\.agent-tab-wrap\.selected\.side-console\{background:rgba\(216,221,227,.16\)\}/);
  assert.match(shell, /button\("", name, `agent-tab/);
  // Item 2go: the tab reads the chat name; the role is the robot glyph and its
  // hover text, which is where it was always readable.
  assert.match(shell, /class="agent-tab-name">\$\{escapeHTML\(name\)\}<\/span>/);
  assert.match(shell, /title="\$\{escapeHTML\(agentID\)\}"/);
  assert.match(shell, /button\("×", `Close \$\{name\}`/);
  assert.match(shell, /const agentID = `agent_\$\{session\?\.role === "d" \? "d" : "b"\}`/);
  // Item 2gl (v1.2.6): the window title names the CHAT; the header beside the
  // tab strip still reads the connection only (2eo).
  assert.match(shell, /document\.title = session \? `Agent_b · \$\{chatName\(session\)\}` : "Agent_b"/);
  // Item 2eo: the header beside the tab strip reads the CONNECTION, through
  // sessionTitle. Item 2hc (v1.3.0/W7) assigns it through a local so the box
  // can be snapped to a whole pixel when the text changes, so the contract is
  // checked by what it computes rather than by one spelling of the statement.
  assert.match(shell, /sessionTitle\(session\)/);
  assert.match(shell, /sessionHeading\.textContent = heading/);
  // The snap itself: a fractional title width became the strip's left edge and
  // the title's text then rasterised at a subpixel phase, which made two
  // captures of one build disagree. Rounding up puts every item to its right on
  // whole pixels too.
  assert.match(shell, /sessionHeading\.style\.width = `\$\{Math\.ceil\(natural\)\}px`/);
});

test("plus adds a two-line d choice only for an assigned d connection", () => {
  assert.match(shell, /const hasD = !!String\(configured\?\.d \|\| ""\)\.trim\(\)/);
  assert.match(shell, /agent_b · \$\{name\} — chat/);
  assert.match(shell, /agent_d · \$\{name\} — plan/);
  assert.match(shell, /createChat\("agent_d", configured\)/);
  assert.doesNotMatch(shell, /planRepoEditor|showPlanMenu|plan_id/);
});

test("Plan is a compact accessible chip icon", () => {
  assert.match(shell, /class="shell-page-chip"/);
  assert.match(shell, /setAttribute\("aria-label", "plan"\)/);
  assert.match(shell, /link\.title = "plan"/);
  assert.doesNotMatch(shell, /\[\["plan", "Plan", "\/plan"\]\]/);
  assert.match(shell, /node\("button", `shell-page/);
  assert.match(shell, /node\("button", "shell-settings"\)/);
  assert.doesNotMatch(shell, /link\.href|settings\.href/);
});

test("compact window controls continue the top strip", () => {
  assert.match(shell, /\["minimize", "maximize", "close"\]/);
  assert.match(tokens, /\.shell-window-controls\{[^}]*grid-template-columns:repeat\(3,28px\)/);
  assert.match(tokens, /\.shell-window-control\{[^}]*width:28px/);
  assert.match(tokens, /\.shell-window-control-glyph\{[^}]*width:9px;height:9px/);
});

// Items 2go, 2gx and 2hq: the row switches/reopens the chat; close retains it;
// and only a closed row receives the intentional Delete control.
test("agent menu separates close, reopen and delete", () => {
  assert.match(shell, /sessionsFor\(agentID, true\)/);
  assert.match(shell, /oncontextmenu/);
  assert.match(shell, /\/reopen`/);
  assert.match(shell, /button\("×"/);
  assert.match(shell, /button\("Rename"/);
  assert.match(shell, /agent-chat-rename-form/);
  assert.match(shell, /\{ label \}/);
  assert.match(shell, /summary\.onclick/);
  assert.match(shell, /if \(session\.closed\) row\.append\(remove\)/);
  assert.doesNotMatch(shell, /button\("Open"/);
  assert.doesNotMatch(shell, /button\("🗑"/);
  assert.doesNotMatch(shell, /agent-chat-count/);
  assert.doesNotMatch(shell, /drop_memory/);
  assert.match(shell, /await api\(`\/api\/sessions\/\$\{encodeURIComponent\(session\.id\)\}\/close`, \{\}\)/);
  assert.match(shell, /window\.confirm\(deleteConfirmText\)/);
  assert.match(shell, /Delete this chat\? Its memory notes, plans and files stay\./);
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

test("top-right connection label is the b-role model switcher", () => {
  assert.match(shell, /button\("", "Switch model", "shell-session-title"\)/);
  assert.match(shell, /for \(const connection of store\.connections \|\| store\.config\.connections \|\| \[\]\)/);
  assert.match(shell, /connection\.label \|\| connection\.id[\s\S]*new URL\(host\)\.host[\s\S]*connectionState\(connection, session\)/);
  assert.match(shell, /if \(isRunning\(current\)\)[\s\S]*stop the run first/);
  assert.match(shell, /api\(`\/api\/agents\/\$\{encodeURIComponent\(agentID\)\}\/connection`, \{ action: "set", connection_id: connection\.id \}\)/);
  assert.match(shell, /session\.runnable === false \? session\.not_runnable_reason : sessionTitle\(session\)/);
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

// Item 2ge (v1.1.2/W6), glyph replaced by 2he (v1.3.0/W2): the Plan toggle is
// the processor chip in the operator's accent, and that accent has exactly one
// use in the product.
test("the Plan toggle is the chip in the accent, and the accent is used once", async () => {
  assert.match(shell, /class="shell-page-chip"/);
  assert.doesNotMatch(shell, /M9 4\.5A3\.5 3\.5 0 0 0 5\.5 8/); // the mark he did not recognise
  assert.doesNotMatch(shell, /shell-page-brain/); // 2he: the traced brain is gone
  assert.match(tokens, /--accent-plan:#5AC8FA;/);
  // Item 2le: the chip is the operator's own artwork now, so it carries its own
  // colour and needs no stroke. The accent keeps exactly one use, on the Plan
  // header the full-quality version of that same artwork heads.
  assert.match(tokens, /\.shell-page-chip\{display:block;width:12px;height:12px;opacity:\.6\}/);
  assert.match(tokens, /\.shell-page:hover \.shell-page-chip\{opacity:\.8\}/);
  assert.match(tokens, /\.shell-page\.selected \.shell-page-chip\{opacity:1\}/);
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
  assert.match(shell, /src="\/static\/assets\/plan-mark-nav\.png"/);
  assert.match(shell, /class="shell-page-chip"/);
  // The drawing it replaces is not left behind beside it.
  assert.doesNotMatch(shell, /<rect x="7" y="7" width="10" height="10" rx="2"\/>/);
  assert.doesNotMatch(shell, /stroke-width="2"/);
  // Both sizes ship, and both are real PNGs with an alpha channel: the background
  // is removed, not repainted, so the mark sits on whatever is behind it.
  for (const [name, expected] of [["plan-mark-nav.png", 24], ["plan-mark.png", 128]]) {
    const png = await readFile(new URL(`../assets/${name}`, import.meta.url));
    assert.equal(png.subarray(1, 4).toString("ascii"), "PNG", `${name} is not a PNG`);
    assert.equal(png.readUInt32BE(16), expected, `${name} is not ${expected}px wide`);
    assert.equal(png.readUInt8(25), 6, `${name} has no alpha channel`);
  }
  const tokensCss = await readFile(new URL("../css/tokens.css", import.meta.url), "utf8");
  const chipRule = (tokensCss.split("}").find((rule) => rule.includes(".shell-page-chip{")) ?? "")
    .replace(/\/\*[\s\S]*?\*\//g, "").split(".shell-page-chip{").pop();
  assert.doesNotMatch(chipRule, /stroke/, "the artwork carries its own colour; no stroke is applied to it");
  // The operator explicitly halved the displayed box; that is unchanged.
  assert.match(chipRule, /width:12px;height:12px/);
});

// Item 2gk (v1.3.0/W2): the readout joined the strip, and the marker that drew
// tofu is gone. Operator, 2026-09-22, with a screenshot of the line sitting as
// a header above the transcript: "i want this moved".
test("the attachment control replaces the redundant per-chat readout", async () => {
  const html = await readFile(new URL("../index.html", import.meta.url), "utf8");
  const strip = html.slice(html.indexOf('id="chat-status-strip"'), html.indexOf("</div>", html.indexOf('id="chat-status-strip"')));
  assert.ok(strip.includes('id="chat-attach"'), "the attachment control belongs to the status strip");
  assert.ok(!html.includes('id="chat-readout"'), "the duplicate token readout is gone");
  // It must sit in the composer footer, not between transcript and composer.
  const log = html.indexOf('id="chat-log"');
  const composer = html.indexOf('id="chat-composer"');
  const attachment = html.indexOf('id="chat-attach"');
  assert.ok(attachment > composer, "the attachment control must live inside the composer footer");
  assert.ok(log < composer, "the transcript still precedes the composer");

  const chatCss = await readFile(new URL("../css/chat.css", import.meta.url), "utf8");
  assert.match(chatCss, /\.chat-status-strip \.chat-attach-wrap \{ margin-left:auto; margin-right:-4px; \}/);
  // No control characters anywhere in the stylesheet: the marker was a raw
  // 0x15 byte, which is how it reached the operator's screen as tofu.
  const control = [...chatCss].filter((ch) => ch.charCodeAt(0) < 32 && !"\r\n\t".includes(ch));
  assert.equal(control.length, 0, `stylesheet holds ${control.length} control character(s)`);
  assert.doesNotMatch(chatCss, /\.chat-readout > summary::before/, "the removed readout cannot reintroduce tofu");
});
