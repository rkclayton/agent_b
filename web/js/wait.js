// Item 2m4: ONE WAITING ELEMENT, used everywhere.
//
// The product had no general one: a run showed a blinking robot glyph, a queued
// message showed a sentence, a model load showed nothing at all, and each
// surface invented its own. This is the one, and it is built to the vocabulary
// the UI already speaks — monospace, the six colours, and step timing rather
// than smooth easing, because every other animation in this product moves in
// discrete frames (chat-run-robot is `steps(1, end)`), and a smoothly sweeping
// bar would be the only organic thing on the screen.
//
// (b) IT SAYS WHAT IS HAPPENING. The element cannot be built without a line: an
// animation alone is decoration, and a reader watching a decoration cannot tell
// a slow model from a hung one.
//
// (c) TWO HONEST MODES. Determinate when something real reports progress —
// llama.cpp streams prompt_progress with processed and total, which
// rel-1.23.0/W0 confirmed is the only progress any server in this product
// reports — and indeterminate when nothing does. An indeterminate wait NEVER
// animates toward a finish it does not know: it cycles a fixed pattern in place,
// which says "working" without implying "nearly there".

const CELLS = 12;

export function waitElement(document, { line, processed = null, total = null } = {}) {
  if (!line || !String(line).trim()) {
    // (b) enforced in code rather than asked for in a comment.
    throw new Error("a waiting element must say what is being waited for");
  }
  const root = document.createElement("div");
  root.className = "wait";
  root.setAttribute("role", "status");

  const meter = document.createElement("div");
  meter.className = "wait-meter";
  for (let index = 0; index < CELLS; index++) {
    const cell = document.createElement("span");
    cell.className = "wait-cell";
    cell.style.setProperty("--cell", String(index));
    meter.append(cell);
  }

  const label = document.createElement("span");
  label.className = "wait-line";
  label.textContent = String(line);

  root.append(meter, label);
  setWaitProgress(root, { processed, total });
  return root;
}

// setWaitProgress moves an element between the two modes. Passing a total of
// zero or nothing returns it to indeterminate, which is what happens when a
// server that was reporting progress stops.
export function setWaitProgress(root, { processed = null, total = null } = {}) {
  const known = Number.isFinite(Number(total)) && Number(total) > 0 && Number.isFinite(Number(processed));
  if (!known) {
    if (root.dataset.mode !== "indeterminate") root.dataset.mode = "indeterminate";
    if (root.hasAttribute("aria-valuenow")) root.removeAttribute("aria-valuenow");
    // `delete` on a dataset removes the attribute, which removeAttribute would
    // also do — but this keeps the read and the write in one vocabulary.
    root.querySelectorAll(".wait-cell").forEach((cell) => { if (cell.hasAttribute("data-lit")) delete cell.dataset.lit; });
    return root;
  }
  const fraction = Math.max(0, Math.min(1, Number(processed) / Number(total)));
  const lit = Math.round(fraction * CELLS);
  if (root.dataset.mode !== "determinate") root.dataset.mode = "determinate";
  // Lit per cell rather than through a custom property, because CSS cannot read
  // a var() inside :nth-child and a rule that silently matches nothing is worse
  // than no rule.
  const cells = root.querySelectorAll(".wait-cell");
  cells.forEach((cell, index) => {
    const value = index < lit ? "1" : "0";
    if (cell.dataset.lit !== value) cell.dataset.lit = value;
  });
  const now = String(Math.round(fraction * 100));
  if (root.getAttribute("aria-valuenow") !== now) root.setAttribute("aria-valuenow", now);
  return root;
}

export const WAIT_CELLS = CELLS;
