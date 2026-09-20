import { store } from "./bus.js";
import { initShell } from "./shell.js";
import { closeSettings, initSettings } from "./settings.js";
import { mountChat } from "./chat.js";

// Item 2gk (v1.2.3): there is one surface in this document now. The page that
// stood beside the chat is gone; its six groups are two sections of Settings,
// and the numbers that describe THIS chat are on the chat itself. So this file
// no longer switches between two surfaces — it shows the chat, and the only
// other destination, Plan, is its own document.
const chatSurface = [
  document.getElementById("chat-budget"),
  document.getElementById("chat-log"),
  document.getElementById("chat-readout"),
  document.getElementById("chat-composer"),
];
let shell;

// Item 2gn: the id the operator opened the page with. The first show() runs
// before any snapshot, when the store holds no selection, so pathFor used to
// rewrite `/chat?session=<id>` to `/chat` and delete the very id the page was
// asked for — chat.js then had nothing to honour. It is kept until the store
// has a selection of its own.
const openedWith = new URLSearchParams(location.search).get("session") || "";

function pathFor() {
  const sessionID = store.selection.session_id || (store.loaded ? "" : openedWith);
  return `/chat${sessionID ? `?session=${encodeURIComponent(sessionID)}` : ""}`;
}

// `/` was the route of the page now dissolved. It is still served and it still
// shows the chat,
// so a bookmark or a pasted link lands somewhere rather than nowhere.
function show({ history = "push" } = {}) {
  closeSettings("chat");
  document.body.dataset.page = "chat";
  document.body.classList.add("chat-page");
  for (const element of chatSurface) if (element) element.hidden = false;
  mountChat(shell);
  shell.setPage("chat");
  const path = pathFor();
  if (history === "push" && `${location.pathname}${location.search}` !== path) window.history.pushState({ page: "chat" }, "", path);
  else if (history === "replace" && `${location.pathname}${location.search}` !== path) window.history.replaceState({ page: "chat" }, "", path);
}

shell = initShell({
  page: "chat",
  switchView: () => show({}),
  syncLocation: () => {
    const path = pathFor();
    if (`${location.pathname}${location.search}` !== path) window.history.replaceState({ page: "chat" }, "", path);
  },
});
initSettings();
show({ history: "replace" });
window.addEventListener("popstate", () => show({ history: "none" }));
