import { renderContextPage } from "./settings-context.js";
import { renderRunPage } from "./settings-run.js";

// Item 2lj (a): TWO ROWS, no more. Text size is a set of named steps rather than
// a number, and typeface is a plain list of names -- no badge, no note, nothing
// marking any one of them as an accessibility option, because the operator asked
// for exactly that: "just include it as a font dont make it special or anything".
const TEXT_SIZES = ["small", "normal", "large", "larger", "largest"];
const TYPEFACES = [
  "IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic",
  "Segoe UI", "Arial", "Verdana", "Tahoma", "Trebuchet MS",
  "Georgia", "Times New Roman", "Calibri", "Cambria", "Comic Sans MS",
];

// Item 2mf (e) and (f): the hide confirmation in the tab strip promises this
// switch exists, so it ships with the same item. Hiding stores a name; showing
// removes it. Nothing about the surface is discarded either way, which is why the
// hint says hidden and not gone.
const HIDEABLE_SURFACES = [["plan", "Plan"]];

function surfaceRow(context) {
  const hidden = new Set(Array.isArray(context.store.config.chat?.hidden_surfaces) ? context.store.config.chat.hidden_surfaces : []);
  return HIDEABLE_SURFACES.map(([kind, label]) => {
    const shown = !hidden.has(kind);
    return context.field(`chat.hidden_surfaces.${kind}`, label,
      `<button type="button" role="switch" aria-checked="${shown}" aria-label="Show the ${label} tab" class="switch ${shown ? "on" : ""}" data-action="surface-visible" data-surface="${kind}" data-value="${shown ? "false" : "true"}"></button>`,
      false, `Shows the ${label} tab, pinned at the right of the tab strip.`);
  }).join("");
}

export function renderChatsPage(active, context) {
  return `${context.subhead("Reading", "How the transcript and the message box are drawn.")}
    ${context.choices("chat.text_size", "text size", TEXT_SIZES, context.store.config.chat?.text_size || "normal", "Scales the transcript, the message box and the step rows together. Nothing else in the product changes size.")}
    ${context.choices("chat.typeface", "typeface", TYPEFACES, context.store.config.chat?.typeface || "IBM Plex Sans", "Applies to the text of a message. Code, tool output and paths stay monospaced.")}
    ${context.subhead("Tabs", "Which pinned tabs the strip shows. A hidden tab is hidden, not gone.")}
    ${surfaceRow(context)}
    ${context.subhead("Run and approval", "Limits and approval behavior shared by every chat.")}
    ${renderRunPage(context)}
    ${context.number("run.max_concurrent", "max concurrent", context.store.config.run?.max_concurrent, "1", false, "", false, "number", "How many chats may run at the same time; the rest wait in the queue.")}
    ${context.subhead("Context", "Compaction thresholds and the active connection's measured limits.")}
    ${renderContextPage(active, context)}`;
}
