import { store } from "./bus.js";
import { initShell } from "./shell.js";
import { closeSettings, initSettings } from "./settings.js";
import { mountConsole, unmountConsole } from "./app.js";
import { mountChat, unmountChat } from "./chat.js";
import { beginNavigation } from "./navigation-telemetry.js";

const consoleSurface = document.getElementById("console-surface");
const consoleStyles = document.getElementById("console-styles");
const chatStyles = document.getElementById("chat-styles");
const chatSurface = [
  document.getElementById("chat-budget"),
  document.getElementById("chat-log"),
  document.getElementById("chat-composer"),
];
let page = location.pathname === "/chat" ? "chat" : "console";
let shell;

// Item 2gn: the id the operator opened the page with. The first show() runs
// before any snapshot, when the store holds no selection, so pathFor used to
// rewrite `/chat?session=<id>` to `/chat` and delete the very id the page was
// asked for — chat.js then had nothing to honour. It is kept until the store
// has a selection of its own.
const openedWith = new URLSearchParams(location.search).get("session") || "";

function pathFor(next) {
  const sessionID = store.selection.session_id || (store.loaded ? "" : openedWith);
  const suffix = sessionID ? `?session=${encodeURIComponent(sessionID)}` : "";
  return `${next === "chat" ? "/chat" : "/"}${suffix}`;
}

function show(next, { history = "push", measure = false } = {}) {
  if (next !== "chat" && next !== "console") return;
  closeSettings(next);
  const previous = page;
  if (measure && previous !== next) beginNavigation({
    kind: "flip", from: previous, to: next, fullDocument: false,
    chatID: store.selection.session_id, mutationToken: store.mutation_token,
  });
  page = next;
  document.body.dataset.page = next;
  document.body.classList.toggle("chat-page", next === "chat");
  consoleStyles.disabled = next === "chat";
  chatStyles.disabled = next !== "chat";
  consoleSurface.hidden = next !== "console";
  for (const element of chatSurface) element.hidden = next !== "chat";
  if (next === "chat") { unmountConsole(); mountChat(shell); }
  else { unmountChat(); mountConsole(); }
  shell.setPage(next);
  const path = pathFor(next);
  if (history === "push" && `${location.pathname}${location.search}` !== path) window.history.pushState({ page: next }, "", path);
  else if (history === "replace" && `${location.pathname}${location.search}` !== path) window.history.replaceState({ page: next }, "", path);
}

shell = initShell({
  page,
  switchView: (next, navigation) => show(next, { measure: navigation?.kind === "flip" }),
  syncLocation: (current) => {
    const path = pathFor(current);
    if (`${location.pathname}${location.search}` !== path) window.history.replaceState({ page: current }, "", path);
  },
});
initSettings();
show(page, { history: "replace" });
window.addEventListener("popstate", () => show(location.pathname === "/chat" ? "chat" : "console", { history: "none" }));
