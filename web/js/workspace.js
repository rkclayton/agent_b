import { store } from "./bus.js";
import { initShell } from "./shell.js";
import { closeSettings, initSettings, openSettings } from "./settings.js";
import { mountChat } from "./chat.js";
import { recordViewMount } from "./navigation-telemetry.js";

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

// Item 2hb (v1.2.4): THE ADDRESS IS READ ONCE, HERE, BEFORE ANYTHING REWRITES
// IT. That is the whole defect the operator hit: the shell syncs the address to
// the mounted view during its first render, which happens while initShell is
// still running, and that sync deleted the `#settings/...` the gear had just
// put there - so the module that owns Settings read an empty hash and the
// window sat on the chat. Whoever needs the request now gets it as a value, and
// the address is free to be rewritten to whatever is actually up.
const entry = (() => {
  const search = new URLSearchParams(location.search);
  const requested = location.hash.match(/^#settings(?:\/([a-z-]+))?$/);
  const from = search.get("from");
  return {
    settings: requested ? (requested[1] || "connections") : "",
    // The view to return to when Settings closes. Only another document can
    // ask for this; inside this one, Settings closes onto the chat beneath it.
    from: from === "plan" ? "plan" : "",
  };
})();

function pathFor() {
  const sessionID = store.selection.session_id || (store.loaded ? "" : openedWith);
  return `/chat${sessionID ? `?session=${encodeURIComponent(sessionID)}` : ""}`;
}

// Item 2hb: the one view state. The chat is what this document mounts; Settings
// is a sheet over it. Everything that navigates asks this, and nothing keeps a
// second copy to disagree with.
export function mountedView() {
  return document.getElementById("settings-page")?.hidden === false ? "settings" : "chat";
}

// `/` was the route of the page now dissolved. It is still served and it still
// shows the chat,
// so a bookmark or a pasted link lands somewhere rather than nowhere.
function show({ history = "push" } = {}) {
  const started = performance.now();
  closeSettings("chat");
  document.body.dataset.page = "chat";
  document.body.classList.add("chat-page");
  for (const element of chatSurface) if (element) element.hidden = false;
  mountChat(shell);
  shell.setPage("chat");
  recordViewMount("chat", performance.now() - started);
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
initSettings(entry);
show({ history: "replace" });
// The request the address carried, honoured after the chat is up so the sheet
// has something to sit over. It is a value now, not a hash that a later write
// can delete.
if (entry.settings) openSettings(entry.settings);
window.addEventListener("popstate", () => show({ history: "none" }));
