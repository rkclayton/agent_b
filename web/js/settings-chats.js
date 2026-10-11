import { renderContextPage } from "./settings-context.js";
import { renderRunPage } from "./settings-run.js";

const SHIPPED_FACES = ["IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic"];
const NAMED_PIXELS = { small: 11, normal: 12, large: 14, larger: 16, largest: 18 };
export const TEXT_COLORS = [
  ["default", "Default", "#E8EEF4"], ["white", "White", "#FFFFFF"],
  ["red", "Red", "#FF6B6B"], ["orange", "Orange", "#FF9F43"],
  ["yellow", "Yellow", "#FFD166"], ["green", "Green", "#3CF08A"],
  ["cyan", "Cyan", "#4DDFF7"], ["blue", "Blue", "#6CA8FF"],
  ["purple", "Purple", "#C792EA"], ["pink", "Pink", "#FF85C2"],
];

const safeFace = (value) => String(value).replace(/["';]/g, "");

export function renderChatsPage(active, context) {
  const chatCount = Object.keys(context.store.sessions || {}).length;
  const chat = context.store.config.chat || {};
  const size = Number.isInteger(chat.text_size) ? chat.text_size : (NAMED_PIXELS[chat.text_size] || 12);
  const installed = context.fontFamilies();
  const chosen = String(chat.typeface || SHIPPED_FACES[0]);
  const faces = installed.map((family) => ({ value: family, label: family, style: `font-family:&quot;${safeFace(family)}&quot;,sans-serif` }));
  if (!installed.includes(chosen)) faces.unshift({ value: chosen, label: chosen, missing: true, style: 'font-family:"IBM Plex Sans",sans-serif' });
  const colors = TEXT_COLORS.map(([value, label, color]) => ({ value, label, style: `color:${color}` }));
  return `${context.subhead("Reading")}
    ${context.field("chat.text_size", "text size", `<input class="setting-input number" type="number" min="9" max="32" step="1" data-path="chat.text_size" data-kind="number" value="${context.attr(size)}">`, false, "Set transcript text size from 9 to 32 pixels.")}
    ${context.menuSetting("chat.typeface", "typeface", faces, chosen, "font-menu", "Choose the transcript typeface.")}
    ${context.menuSetting("chat.text_color", "text colour", colors, chat.text_color || "default", "colour-menu", "Choose the transcript text colour.")}
    ${renderRunPage(context)}
    ${context.subhead("Context")}
    ${renderContextPage(active, context)}
    ${context.subhead("Delete")}
    ${context.row("all chats", `<button type="button" class="delete-all-chats" data-action="delete-all-chats" ${chatCount ? "" : "disabled"}>Delete all chats</button>`, "", `${chatCount} chat${chatCount === 1 ? "" : "s"}`)}`;
}
