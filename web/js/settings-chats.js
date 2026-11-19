import { renderGeneralPage } from "./settings-general.js";
import { renderContextPage } from "./settings-context.js";
import { renderRunPage } from "./settings-run.js";

const SHIPPED_FACES = ["IBM Plex Sans", "Atkinson Hyperlegible", "OpenDyslexic"];
const NAMED_PIXELS = { small: 11, normal: 12, large: 14, larger: 16, largest: 18 };
export const BACKGROUND_COLORS = [["graphite","Graphite","#0D1014"],["black","Black","#060709"],["midnight","Midnight","#0B1020"],["deep-sea","Deep sea","#08161A"],["forest","Forest","#0B1510"],["wine","Wine","#170D12"],["umber","Umber","#17120B"]];
export function applyBackground(value, root=globalThis.document?.documentElement) {
  const colour=(BACKGROUND_COLORS.find(row=>row[0]===value)||BACKGROUND_COLORS[0])[2];
  const rgb=[1,3,5].map(at=>parseInt(colour.slice(at,at+2),16));
  const offset=delta=>"#"+rgb.map((v,i)=>(v+delta[i]).toString(16).padStart(2,"0")).join("");
  const palette={"--bezel":colour,"--bezel-rgb":rgb.join(","),"--well":offset([-5,-6,-7]),"--raised-fill":offset([8,8,8]),"--raised-fill-deep":colour};
  if(root)for(const [key,val]of Object.entries(palette))root.style.setProperty(key,val);
  return palette;
}
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
  const faces = installed.map((family) => ({ value: family, label: family, face: safeFace(family) }));
  if (!installed.includes(chosen)) faces.unshift({ value: chosen, label: chosen, missing: true, face: "IBM Plex Sans" });
  const colors = TEXT_COLORS.map(([value, label, color]) => ({ value, label, color }));
  const deleteError = context.errors.get("chat_root");
  return `${context.subhead("Reading")}
    ${context.field("chat.text_size", "text size", `<input class="setting-input number" type="number" min="9" max="32" step="1" data-path="chat.text_size" data-kind="number" value="${context.attr(size)}">`, false, "Set transcript text size from 9 to 32 pixels.")}
    ${context.menuSetting("chat.typeface", "typeface", faces, chosen, "font-menu", "Choose the transcript typeface.")}
    ${context.menuSetting("chat.text_color", "text colour", colors, chat.text_color || "default", "colour-menu", "Choose the transcript text colour.")}
    ${context.menuSetting("background_color", "background colour", BACKGROUND_COLORS.map(([value,label])=>({value,label})), context.store.config.background_color || "graphite", "colour-menu", "Choose the window background colour.")}
    ${renderRunPage(context)}
    ${context.subhead("Context")}
    ${renderContextPage(active, context)}
    ${context.subhead("Delete")}
    ${deleteError ? context.errorMarkup(deleteError, "group:chat_root", "settings-group-error field-error") : ""}
    ${context.row("all chats", `<button type="button" class="delete-all-chats" data-action="delete-all-chats" ${chatCount ? "" : "disabled"}>Delete all chats</button>`, "", `${chatCount} chat${chatCount === 1 ? "" : "s"}`)}${context.subhead("General")}${renderGeneralPage("limits",active,context)}`;
}
import { actionTable } from "./settings-action-table.js";

export const chatActions = actionTable([
  "delete-all-chats", "surface-visible", "new-session", "close-session",
  "reset-session",
]);
