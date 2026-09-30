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
//
// Item 2ni: the tab it hid is gone, so this switch now hides the Plan's SETTINGS
// SECTION. The stored name and the config key are unchanged; only what the choice
// applies to moved, along with the Plan itself.
const HIDEABLE_SURFACES = [["plan", "Plan"]];

function surfaceRow(context) {
  const hidden = new Set(Array.isArray(context.store.config.chat?.hidden_surfaces) ? context.store.config.chat.hidden_surfaces : []);
  return HIDEABLE_SURFACES.map(([kind, label]) => {
    const shown = !hidden.has(kind);
    return context.field(`chat.hidden_surfaces.${kind}`, label,
      `<button type="button" role="switch" aria-checked="${shown}" aria-label="Show ${label} in Settings" class="switch ${shown ? "on" : ""}" data-action="surface-visible" data-surface="${kind}" data-value="${shown ? "false" : "true"}"></button>`,
      false, `Shows ${label} as its own section in this Settings nav. Off hides the entry; nothing about the page is discarded and its address still works.`);
  }).join("");
}

// Item 2mx (d): the chat a spoken request lands in, settable here. Empty means the
// server creates one labelled Siri the first time a spoken request arrives and records
// it — so this is normally filled in by having used it, and is here to point it
// somewhere else or to clear it.
function voiceRow(context) {
  const sessions = Object.values(context.store.sessions || {}).filter((one) => one && !one.closed);
  const current = context.store.config.voice?.default_session_id || "";
  const options = [["", "a chat named Siri, created when first needed"]]
    .concat(sessions.map((one) => [one.id, `${one.label || one.id} (${one.id})`]));
  // A configured chat that has since been closed is still shown, so the field says what
  // it holds rather than silently reading as empty.
  if (current && !sessions.some((one) => one.id === current)) options.push([current, `${current} (closed)`]);
  // A select rather than the button row: a chat list is a list, not a handful of modes.
  return context.selectSetting("voice.default_session_id", "voice chat", options, current);
}

export function renderChatsPage(active, context) {
  return `${context.subhead("Chats", "The directory Explorer and the chat menu share.")}
	${context.field("chat_root", "root path", `<span class="path">${context.html(context.store.chat_root || "")}</span>`, false, "Read-only. Open this path in Explorer to see the same chat tree.")}
	${context.subhead("Reading", "How the transcript and the message box are drawn.")}
    ${context.choices("chat.text_size", "text size", TEXT_SIZES, context.store.config.chat?.text_size || "normal", "Scales the transcript, the message box and the step rows together. Nothing else in the product changes size.")}
    ${context.choices("chat.typeface", "typeface", TYPEFACES, context.store.config.chat?.typeface || "IBM Plex Sans", "Applies to the text of a message. Code, tool output and paths stay monospaced.")}
    ${context.subhead("Voice", "Where a spoken request lands when it names no chat.")}
    ${voiceRow(context)}
    ${context.subhead("Tabs", "Which pinned tabs the strip shows. A hidden tab is hidden, not gone.")}
    ${surfaceRow(context)}
    ${context.subhead("Run and approval", "Limits and approval behavior shared by every chat.")}
    ${renderRunPage(context)}
    ${context.number("run.max_concurrent", "max concurrent", context.store.config.run?.max_concurrent, "1", false, "", false, "number", "How many chats may run at the same time; the rest wait in the queue.")}
    ${context.subhead("Context", "Compaction thresholds and the active connection's measured limits.")}
    ${renderContextPage(active, context)}`;
}
