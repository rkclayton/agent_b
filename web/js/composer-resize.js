// Item 2ld: the strip between the transcript and the composer is the handle.
// Dragging it up makes the message box taller and the transcript shorter, smoothly,
// and the height is remembered for this surface so it is set once rather than every
// time. What is typed is untouched, the transcript is never scrolled, and neither
// area can be dragged away: the composer stops between COMPOSER_MIN and the space
// the window can spare above TRANSCRIPT_MIN.
//
// It lives in its own module because a drag is only proven by dragging it, and the
// gate needs to load this behaviour without loading the whole chat surface.
export const COMPOSER_MIN = 48;
export const TRANSCRIPT_MIN = 120;
export const HEIGHT_KEY = "agentb.composer-height";

export function clampComposerHeight(height, { current, transcript }) {
  const ceiling = Math.max(COMPOSER_MIN, transcript + current - TRANSCRIPT_MIN);
  return Math.round(Math.min(Math.max(height, COMPOSER_MIN), ceiling));
}

export function installComposerResize({ strip, composer, input, log, store = globalThis.localStorage } = {}) {
  if (!strip || !composer || !input) return;

  const currentHeight = () => {
    const raw = Number.parseFloat(composer.style.getPropertyValue("--composer-height"));
    return Number.isFinite(raw) ? raw : input.getBoundingClientRect().height;
  };
  const apply = (height) => {
    const value = clampComposerHeight(height, { current: currentHeight(), transcript: log?.clientHeight || 0 });
    composer.style.setProperty("--composer-height", `${value}px`);
    return value;
  };

  try {
    const remembered = Number.parseFloat(store?.getItem(HEIGHT_KEY) ?? "");
    if (Number.isFinite(remembered)) composer.style.setProperty("--composer-height", `${Math.max(COMPOSER_MIN, remembered)}px`);
  } catch { /* the composer simply keeps the height it has always had */ }

  strip.addEventListener("pointerdown", (event) => {
    // The strip still carries controls; a press on one of them is that control's.
    if (event.button !== 0 || event.target.closest?.("button, a, input, select, textarea")) return;
    const startY = event.clientY;
    const startHeight = currentHeight();
    event.preventDefault();
    strip.classList.add("dragging");
    strip.setPointerCapture?.(event.pointerId);
    const move = (moveEvent) => apply(startHeight + (startY - moveEvent.clientY));
    const finish = () => {
      strip.removeEventListener("pointermove", move);
      strip.removeEventListener("pointerup", finish);
      strip.removeEventListener("pointercancel", finish);
      strip.classList.remove("dragging");
      try { store?.setItem(HEIGHT_KEY, String(Math.round(currentHeight()))); } catch { /* it lasts this session */ }
    };
    strip.addEventListener("pointermove", move);
    strip.addEventListener("pointerup", finish);
    strip.addEventListener("pointercancel", finish);
  });
}
