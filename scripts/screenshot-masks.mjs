// Item 2ga (v1.0.0/W2): the live values a chat-acceptance capture shows. Each
// is found by a selector and, where it is text, a pattern inside that
// selector's text, so the mask follows the value when layout moves it. A
// capture writes the rectangles it found beside itself as <image>.masks.json;
// scripts/screenshot-gate.mjs blanks them in both images before comparing and
// reports every capture whose difference lay only inside them as masked.
export const LIVE_VALUES = [
  // The app writes a duration as a number, a space, then ms or s ("67 ms",
  // "5.2 s"); prose such as "3s" or "1990s" is not one (v1.0.0/W4 cold review).
  { name: "duration", reason: "elapsed and wall times (ms, s) are measured each run", selector: "body", pattern: String.raw`(?<![\w.,])\d[\d,]*(?:\.\d+)? (?:ms|s)\b` },
  { name: "timestamp", reason: "ISO times of last use are the clock's", selector: "body", pattern: String.raw`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z` },
  // The GUID is set in proportional type, so what follows it reflows with its
  // width, even onto the next line: this mask alone runs from the GUID to the
  // end of its text, each line to the right edge of the element holding it.
  { name: "acceptance-folder", reason: "each run's temporary folder name carries a fresh GUID", selector: "body", pattern: String.raw`Agent_b-chat-acceptance-[0-9a-f]{1,32}`, restOfText: true },
  { name: "sandbox-name", reason: "the shell's sandbox is named per run", selector: "body", pattern: String.raw`sandbox agentb-[0-9a-f]+` },
  // Not a live value: two runs of one build (v1.0.0/W2 captures k and l)
  // antialias the composer text box's bottom corners a level or two apart, 2 by 2
  // pixels each. The square masked is 4 by 4 at each bottom corner; the cause
  // is carded, and the rest of the composer is compared exactly.
  // v1.2.2/W3: the variance runs the height of the corner's rounding, not just
  // the four pixels v1.0.0 measured - two runs of one build differed three
  // pixels above the square. The square is the radius, 8 by 8, and the rest of
  // the composer is still compared exactly.
  { name: "composer-corner-variance", reason: "the composer's bottom corners antialias a level or two apart between runs of one build", selector: "#chat-task", corners: 8 },
  { name: "transcript-scrollbar", reason: "the thumb's length and place follow the transcript's height, which live text wraps change", selector: "#chat-log", scrollbar: true },
  // v1.2.2/W3: the unreachable notice elides the host, so the visible run can
  // be "0.1:58507" with no 127. prefix for the old pattern to match, and that
  // one un-masked run was the whole difference between two captures of one
  // build. Same named live value, matched wherever the host is cut.
  { name: "loopback-port", reason: "the fake model server's port is chosen when it starts", selector: "body", pattern: String.raw`(?:[\d.]*\d\.\d+|localhost):\d{2,5}` },
  // v1.2.2/W3: a step summary draws its measured duration only when it rounds
  // to at least a millisecond, so two runs of one build read "2 tool calls · 2
  // failed" and "2 tool calls · 2 failed · 1 ms". The counts are compared as
  // they are; only the seat the duration would occupy - from the end of the
  // last count to the right edge of the line - is masked, and it is masked in
  // every capture whether a duration sits there or not, so the two agree.
  { name: "duration-seat", reason: "a duration under half a millisecond is not drawn at all, so the place one would sit differs between runs", selector: ".chat-step-summary, .chat-response-summary, .chat-tool-group-head", pattern: String.raw`\d[\d,]* (?:failed|rows?|thoughts?|answers?|tool calls?)(?=(?: · [\d,.]+ (?:ms|s))?$)`, afterMatch: true },
  { name: "budget-meter", reason: "the chat's budget fill follows measured token counts", selector: ".chat-budget-fill" },
  // v1.0.0/W5: the staged candidate's prompt measured five tokens more than a
  // working-tree build's, so every token readout and the Console rail's
  // segment widths are live values between builds (2ga's "the meter's numbers").
  { name: "token-count", reason: "token counts follow the measured prompt, which differs between builds of one tree", selector: "body", pattern: String.raw`(?<![\w.,])~?\d[\d,]* (?:/ ~?\d[\d,]*|in · \d[\d,]* out)` },
  { name: "lifetime-ratio", reason: "the Lifetime panel's ratios and per-brief token cost are computed from measured token counts", selector: "#panel-lifetime", pattern: String.raw`(?<![\w.])\d+(?:\.\d+)?%|(?<![\w.,])\d[\d,]* tokens` },
  { name: "context-rail", reason: "the Console rail's segments and numbers follow measured token counts", selector: "#rail .meter, #rail .rail-labels .number, #rail .rail-readout" },
];

// OTHER_CHAT_STATE is declared by the shell-state captures only: they set the
// first chat's robot to each state; another chat's robot shows that chat's
// own state as the server last reported it.
export const OTHER_CHAT_STATE = { name: "other-chat-state", reason: "another chat's robot shows that chat's live state, which the capture does not set", selector: ".agent-tab-wrap ~ .agent-tab-wrap .agent-tab-robot" };

// liveValueRects runs in the page: for every spec, the viewport rectangles of
// its matching text (or of its elements when it has no pattern), each clipped
// to every element around it that clips its overflow, so an ellipsised or
// scrolled-away value masks only what is visible, and text that overflows a
// box that does not clip is still masked where it shows.
function liveValueRects(specs) {
  const clipOf = (element) => {
    let box = { left: 0, top: 0, right: innerWidth, bottom: innerHeight };
    for (let node = element; node && node.nodeType === 1; node = node.parentElement) {
      const style = getComputedStyle(node);
      if (style.overflowX !== "visible" || style.overflowY !== "visible") {
        const r = node.getBoundingClientRect();
        box = { left: Math.max(box.left, r.left), top: Math.max(box.top, r.top), right: Math.min(box.right, r.right), bottom: Math.min(box.bottom, r.bottom) };
      }
    }
    return box;
  };
  const clipped = (r, box) => {
    const left = Math.max(r.left, box.left), top = Math.max(r.top, box.top);
    const right = Math.min(r.right, box.right), bottom = Math.min(r.bottom, box.bottom);
    return right > left && bottom > top ? [left, top, right - left, bottom - top] : null;
  };
  const visible = (element) => {
    const style = getComputedStyle(element);
    return style.visibility !== "hidden" && style.display !== "none";
  };
  return specs.map((spec) => {
    const rects = [];
    for (const root of document.querySelectorAll(spec.selector)) {
      if (spec.corners) {
        const r = root.getBoundingClientRect(), n = spec.corners;
        if (visible(root)) for (const left of [r.left, r.right - n]) { const c = clipped({ left, top: r.bottom - n, right: left + n, bottom: r.bottom }, clipOf(root.parentElement)); if (c) rects.push(c); }
        continue;
      }
      if (spec.scrollbar) {
        const r = root.getBoundingClientRect(), style = getComputedStyle(root);
        const [left, right, top] = [style.borderLeftWidth, style.borderRightWidth, style.borderTopWidth].map(parseFloat);
        const width = root.offsetWidth - root.clientWidth - left - right;
        if (width > 0 && visible(root)) { const c = clipped({ left: r.right - right - width, top: r.top + top, right: r.right - right, bottom: r.top + top + root.clientHeight }, clipOf(root.parentElement)); if (c) rects.push(c); }
        continue;
      }
      if (!spec.pattern) {
        if (visible(root)) for (const r of root.getClientRects()) { const c = clipped(r, clipOf(root)); if (c) rects.push(c); }
        continue;
      }
      const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
      for (let text = walker.nextNode(); text; text = walker.nextNode()) {
        const holder = text.parentElement;
        if (!holder || !visible(holder)) continue;
        const pattern = new RegExp(spec.pattern, "g");
        for (let match = pattern.exec(text.data); match; match = pattern.exec(text.data)) {
          if (!match[0]) { pattern.lastIndex++; continue; }
          const range = document.createRange();
          range.setStart(text, match.index);
          range.setEnd(text, spec.restOfText ? text.data.length : match.index + match[0].length);
          // An afterMatch mask covers the seat AFTER its anchor: from the end
          // of the matched text to the right edge of the element holding it,
          // on the anchor's own line. The anchor itself stays compared, and
          // the seat is masked whether anything sits in it or not.
          if (spec.afterMatch) {
            const holderRight = holder.getBoundingClientRect().right, box = clipOf(holder);
            for (const r of range.getClientRects()) { const c = clipped({ left: r.right, top: r.top, right: holderRight, bottom: r.bottom }, box); if (c) rects.push(c); }
            continue;
          }
          // A restOfText mask runs from the match to the end of its text, each
          // line to the right edge of the element holding it; any other covers
          // the matched text only.
          const box = clipOf(holder), end = spec.restOfText ? holder.getBoundingClientRect().right : -Infinity;
          for (const r of range.getClientRects()) { const c = clipped({ left: r.left, top: r.top, right: Math.max(r.right, end), bottom: r.bottom }, box); if (c) rects.push(c); }
        }
      }
    }
    return { name: spec.name, reason: spec.reason, rects };
  });
}

// captureWithMasks takes the screenshot (the page's viewport, or one element's
// box) and writes its mask sidecar. Rectangles are image pixels, rounded
// outward, relative to the element when there is one.
export async function captureWithMasks(target, path, { specs = LIVE_VALUES } = {}) {
  const { writeFile } = await import("node:fs/promises");
  const page = typeof target.page === "function" ? target.page() : target;
  const box = page === target ? null : await target.boundingBox();
  const [ratio, found] = await Promise.all([page.evaluate(() => devicePixelRatio), page.evaluate(liveValueRects, specs)]);
  // Finite CSS transitions are finished first: a capture taken mid-transition
  // differs from the next by a level or two on an edge.
  const image = await target.screenshot({ path, animations: "disabled" });
  const origin = box ? [box.x, box.y] : [0, 0];
  const masks = found.map(({ name, reason, rects }) => ({
    name,
    reason,
    rects: rects.map(([x, y, w, h]) => {
      const left = Math.floor((x - origin[0]) * ratio), top = Math.floor((y - origin[1]) * ratio);
      return [left, top, Math.ceil((x - origin[0] + w) * ratio) - left, Math.ceil((y - origin[1] + h) * ratio) - top];
    }),
  })).filter((mask) => mask.rects.length);
  await writeFile(`${path}.masks.json`, `${JSON.stringify({ element: box ? true : false, masks }, null, 1)}\n`);
  return image;
}
