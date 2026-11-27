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

function createView(document, key, expanded, rerender) {
  const root = document.createElement("div");
  const button = document.createElement("button");
  button.type = "button";
  const caret = document.createElement("span");
  caret.className = "disclosure-caret";
  caret.setAttribute("aria-hidden", "true");
  const summary = document.createElement("span");
  summary.className = "chat-work-phrase";
  const time = document.createElement("span");
  time.className = "chat-work-time";
  button.append(caret, summary, time);
  button.onclick = () => {
    expanded.has(key) ? expanded.delete(key) : expanded.add(key);
    rerender();
  };
  const body = document.createElement("pre");
  body.className = "thinking-body";
  // When the browser's find reveals a collapsed thought, the fold's own state
  // follows it, so the next render does not fold it back under the match.
  body.addEventListener("beforematch", () => {
    expanded.add(key);
    rerender();
  });
  root.append(button, body);
  return { root, button, caret, summary, time, body };
}

function updateView(view, entry, tokens, options) {
  const open = options.expanded.has(entry.key);
  setAttribute(view.button, "class", `thinking-line chat-work-row ${entry.done ? "thought-line" : "thinking-active"}`);
  setAttribute(view.button, "aria-expanded", String(open));
  setText(view.caret, open ? "▾" : "▸");
  setText(view.summary, entry.done ? "Thought" : "Thinking");
  setText(view.time, entry.done ? options.formatDuration?.(entry.thinkingMS) || "" : "");
  setProperty(view.button, "hidden", !!entry.liveStatus);
  let count = "";
  if (entry.done) {
    count = options.uncounted?.(entry)
      ? "thoughts uncounted"
      : `${entry.reasoningTokensEstimated || entry.thinkingEstimated ? "~" : ""}${options.format(tokens)} tokens`;
  }
  // a collapsed thought is real model output, and the
  // audit found the browser's own find could not reach it — `hidden` removes
  // it from the find. `until-found` keeps it out of the layout but inside the
  // search, and the browser reveals it when it matches, which is what 2gg's
  // "find reaches every kind" asks for without adding a control.
  if (open) {
    if (view.body.hasAttribute("hidden")) view.body.removeAttribute("hidden");
  } else setAttribute(view.body, "hidden", "until-found");
  const body = entry.reasoning || (entry.done
    ? "Reasoning text is unavailable in this recording."
    : "Waiting for reasoning text…");
  const text = entry.done ? `${count}\n${body}` : body;
  if (view.body.textContent !== text) view.body.textContent = text;
}

function setText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

function setAttribute(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}

function setProperty(node, name, value) {
  if (node[name] !== value) node[name] = value;
}
