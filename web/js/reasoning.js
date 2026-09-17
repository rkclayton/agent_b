export function createThinkingRenderer(options) {
  const views = new Map();
  let used = new Set();

  function begin() {
    used = new Set();
  }

  function render(entry, tokens) {
    let view = views.get(entry.key);
    if (!view) {
      view = createView(options.document, entry.key, options.expanded, options.rerender);
      views.set(entry.key, view);
    }
    used.add(entry.key);
    updateView(view, entry, tokens, options);
    return view.root;
  }

  function end() {
    for (const key of views.keys()) if (!used.has(key)) views.delete(key);
  }

  return { begin, render, end };
}

const THOUGHT_BUBBLE =
  '<svg class="thought-bubble" viewBox="0 0 16 12" width="11" height="9" aria-hidden="true" focusable="false"><path d="M4.2 7.4a2.4 2.4 0 0 1 .5-4.3 3 3 0 0 1 5.6-.6 2.6 2.6 0 0 1 1.5 4.9z" fill="none" stroke="currentColor" stroke-width="1" stroke-linejoin="round"/><circle cx="4" cy="9.4" r="1.15" fill="none" stroke="currentColor" stroke-width="1"/><circle cx="1.6" cy="11" r="0.7" fill="none" stroke="currentColor" stroke-width="0.9"/></svg>';

function createView(document, key, expanded, rerender) {
  const root = document.createElement("div");
  const button = document.createElement("button");
  button.type = "button";
  const caret = document.createElement("span");
  caret.className = "disclosure-caret";
  // One reasoning mark, used at rest and while live: a thought bubble in line
  // art, palette colours only. It replaces the animated "..." that nobody could
  // read as "the model is thinking".
  const glyph = document.createElement("span");
  glyph.className = "thought-glyph";
  glyph.innerHTML = THOUGHT_BUBBLE;
  const active = document.createElement("span");
  active.textContent = "Thinking";
  const summary = document.createElement("em");
  button.append(caret, glyph, active, summary);
  button.onclick = () => {
    expanded.has(key) ? expanded.delete(key) : expanded.add(key);
    rerender();
  };
  const body = document.createElement("pre");
  body.className = "thinking-body";
  const collapse = document.createElement("button");
  collapse.type = "button";
  collapse.className = "collapse-arrow";
  collapse.textContent = "↑";
  collapse.setAttribute("aria-label", "Collapse thought");
  collapse.onclick = () => { expanded.delete(key); rerender(); };
  root.append(button, collapse, body);
  return { root, button, caret, glyph, active, summary, body, collapse };
}

function updateView(view, entry, tokens, options) {
  const open = options.expanded.has(entry.key);
  setAttribute(view.button, "class", `thinking-line ${entry.done ? "thought-line" : "thinking-active"}`);
  setAttribute(view.button, "aria-expanded", String(open));
  // Item 2eo: "use the thought icon as the caret". The glyph is the disclosure
  // mark (Mute closed, Ink open, Trace while live); the caret node stays, hidden,
  // so the row keeps its structure and hit target.
  setText(view.caret, open ? "▾" : "▸");
  view.caret.hidden = true;
  setAttribute(view.glyph, "data-open", String(open));
  view.active.hidden = entry.done;
  view.summary.hidden = !entry.done;
  if (entry.done) {
    const duration = options.formatDuration(entry.thinkingMS);
    const elapsed = duration ? ` ${entry.thinkingEstimated ? "~" : ""}${duration}` : "";
    const count = options.uncounted?.(entry)
      ? "thoughts uncounted"
      : `${entry.reasoningTokensEstimated || entry.thinkingEstimated ? "~" : ""}${options.format(tokens)} tokens`;
    setText(view.summary, `Thought${elapsed} (${count})`);
  }
  view.body.hidden = !open;
  view.collapse.hidden = !open;
  const body = entry.reasoning || (entry.done
    ? "Reasoning text is unavailable in this recording."
    : "Waiting for reasoning text…");
  if (view.body.textContent !== body) view.body.textContent = body;
}

function setText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

function setAttribute(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}
