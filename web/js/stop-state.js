const activeStatuses = new Set(["running", "queued", "paused", "stopping"]);

export function projectStopState(session, replay = false) {
  const status = session?.run?.status || "idle";
  const stopping = !replay && status === "stopping";
  const active = !replay && !!session && activeStatuses.has(status);
  return {
    active,
    disabled: !active,
    state: stopping ? "stopping" : active ? "active" : "idle",
    label: stopping ? "Emergency stop — cancel immediately" : active ? "Stop active run — cancel and hold queued messages" : "No active run to stop",
  };
}

// Item 2ge: send and stop are ONE control. This projects which of the two it
// is right now, so the button job is never guessed from two places.
export function projectSendStopState(session, replay = false) {
  const stop = projectStopState(session, replay);
  if (stop.active) return { mode: "stop", ...stop };
  return {
    mode: "send",
    active: false,
    disabled: !session || !!replay,
    state: "idle",
    label: "Send \u00b7 Enter sends \u00b7 Shift+Enter newline",
  };
}

// renderSendStop draws whichever state it is on the one button.
export function renderSendStop(button, session, replay = false) {
  const state = projectSendStopState(session, replay);
  setProperty(button, "disabled", state.disabled);
  button.classList.toggle("stop-sign", state.mode === "stop");
  button.classList.toggle("active", state.state === "active");
  button.classList.toggle("stopping", state.state === "stopping");
  setAttribute(button, "data-state", state.state);
  setAttribute(button, "data-mode", state.mode);
  setAttribute(button, "aria-label", state.mode === "stop" ? state.label : "Send");
  setAttribute(button, "title", state.label);
  // The glyph IS the state: the enter mark when it sends, and the octagon with
  // its inner square while a run is live. Item 2ha: the enter mark is an SVG in
  // the document, one of the three controls that are now one family, so it is
  // never written over with text - the square is added beside it and CSS hides
  // the glyph while the octagon is on.
  const square = button.querySelector(":scope > span[aria-hidden]");
  if (state.mode === "stop") {
    if (!square) {
      const mark = button.ownerDocument.createElement("span");
      mark.setAttribute("aria-hidden", "true");
      button.append(mark);
    }
  } else if (square) {
    square.remove();
  }
  return state;
}

export function renderStopState(button, session, replay = false) {
  const state = projectStopState(session, replay);
  setProperty(button, "disabled", state.disabled);
  button.classList.toggle("active", state.state === "active");
  button.classList.toggle("stopping", state.state === "stopping");
  setAttribute(button, "data-state", state.state);
  setAttribute(button, "aria-label", state.label);
  setAttribute(button, "title", state.label);
  return state;
}

function setAttribute(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}

function setProperty(node, name, value) {
  if (node[name] !== value) node[name] = value;
}
