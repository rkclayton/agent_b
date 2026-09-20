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
  button.disabled = state.disabled;
  button.classList.toggle("stop-sign", state.mode === "stop");
  button.classList.toggle("active", state.state === "active");
  button.classList.toggle("stopping", state.state === "stopping");
  button.dataset.state = state.state;
  button.dataset.mode = state.mode;
  button.setAttribute("aria-label", state.mode === "stop" ? state.label : "Send");
  button.setAttribute("title", state.label);
  // The glyph IS the state: the enter mark when it sends, and the octagon with
  // its inner square while a run is live. The square is a child element,
  // because that is what .stop-sign styles, so it is managed rather than
  // written over with text.
  if (state.mode === "stop") {
    if (button.firstElementChild?.tagName !== "SPAN") {
      button.textContent = "";
      const square = button.ownerDocument.createElement("span");
      square.setAttribute("aria-hidden", "true");
      button.append(square);
    }
  } else if (button.textContent !== "\u21b5") {
    button.textContent = "\u21b5";
  }
  return state;
}

export function renderStopState(button, session, replay = false) {
  const state = projectStopState(session, replay);
  button.disabled = state.disabled;
  button.classList.toggle("active", state.state === "active");
  button.classList.toggle("stopping", state.state === "stopping");
  button.dataset.state = state.state;
  button.setAttribute("aria-label", state.label);
  button.setAttribute("title", state.label);
  return state;
}
