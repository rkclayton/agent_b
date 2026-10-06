import { api, reduce, setSelection, setSurface, store, subscribe } from "./bus.js";
import { chatName, chatRowText, isRunning, sessionTitle } from "./chat-lifecycle.js";
import { installUIErrorRelay } from "./ui-error-relay.js";
import { installPageHealth } from "./page-health.js";
import { requestNavigation } from "./navigation-guard.js";
import { surfaceForPage } from "./surfaces.js";
import { connectionHealth } from "./settings-connections.js";
import { beginNavigation } from "./navigation-telemetry.js";
import { arrangeChats, menuLabels, panelDrag } from "./chat-list.js";

const activeRunStates = new Set(["running", "queued", "stopping"]);
const agentKey = (agent) => String(agent?.name || "").trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
// Item 2gk (v1.2.3): a chat had two sides and the shell remembered which one
// you were last on. There is one side now, so there is nothing to remember and
// nothing to flip to.

export function initShell(options = {}) {
	installUIErrorRelay({ token: () => store.mutation_token, sessionID: () => store.active });
	installPageHealth({ token: () => store.mutation_token });
  const root = document.getElementById("app-shell");
  if (!root) return null;
  let page = options.page || root.dataset.page || "chat";
  root.replaceChildren();
  // Item 2mf (b): the document loaded ON a surface, so the selection says which
  // one. A chat page leaves the chat selection alone; a static surface records
  // itself, which is how its tab knows it is the selected one after a reload.
  const loaded = surfaceForPage(page, store.selection.session_id);
  if (loaded && loaded.kind !== "chat") setSurface(loaded);

  const left = node("div", "shell-left");
  const newChatButton = button("New chat", "New chat with agent_b", "chat-list-new");
  const newChatMenu = node("div", "shell-menu shell-new-menu");
  newChatMenu.hidden = true;
  const menuAnchors = new WeakMap();
  let chatTree = { folders: [], chats: [] };
  let chatTreeLoaded = false;
  let chatTreeRefresh = null;

  const chatPanel = node("aside", "chat-list-panel");
  chatPanel.setAttribute("aria-label", "Chats");
  const chatList = node("div", "chat-list");
  const panelEdge = node("div", "chat-list-resize");
  panelEdge.setAttribute("role", "separator");
  panelEdge.setAttribute("aria-orientation", "vertical");
  const panelHandle = node("div", "chat-list-handle");
  panelHandle.setAttribute("role", "separator");
  panelHandle.setAttribute("aria-label", "Show chats");
  chatPanel.append(newChatButton, newChatMenu, chatList, panelEdge);
  if (page === "chat") { root.after(chatPanel); document.body.append(panelHandle); }

  const panelKey = "agentb.chat-list";
  let panel = { width: 240, hidden: false };
  try { panel = { ...panel, ...JSON.parse(localStorage.getItem(panelKey) || "{}") }; } catch {}
  const applyPanel = () => {
    const settingsShown = !!document.querySelector("#settings-page:not([hidden])");
    document.documentElement.style.setProperty("--chat-list-width", `${panel.width}px`);
    document.body.classList.toggle("chat-list-hidden", panel.hidden);
    document.body.classList.toggle("chat-list-visible", !panel.hidden && page === "chat" && !settingsShown);
    chatPanel.hidden = panel.hidden || page !== "chat" || settingsShown;
    panelHandle.hidden = !panel.hidden || page !== "chat" || settingsShown;
  };
  const resizePanel = (handle) => handle.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    const start = { ...panel }, x = event.clientX;
    handle.setPointerCapture?.(event.pointerId);
    const move = (next) => { panel = panelDrag(start, next.clientX - x, innerWidth); applyPanel(); };
    const finish = () => {
      handle.removeEventListener("pointermove", move); handle.removeEventListener("pointerup", finish); handle.removeEventListener("pointercancel", finish);
      try { localStorage.setItem(panelKey, JSON.stringify(panel)); } catch {}
    };
    handle.addEventListener("pointermove", move); handle.addEventListener("pointerup", finish); handle.addEventListener("pointercancel", finish);
  });
  resizePanel(panelEdge); resizePanel(panelHandle); applyPanel();

  const right = node("div", "shell-right");
  const sessionHeading = button("", "Switch model", "shell-session-title");
	let headerTelemetry = "";
  const connectionMenu = node("div", "shell-menu shell-connection-menu");
  connectionMenu.hidden = true;
  sessionHeading.setAttribute("aria-haspopup", "menu");
  sessionHeading.setAttribute("aria-expanded", "false");
  sessionHeading.onclick = () => {
    if (connectionMenu.hidden) {
      renderConnectionMenu();
      revealMenu(connectionMenu, sessionHeading);
      sessionHeading.setAttribute("aria-expanded", "true");
    } else {
      connectionMenu.hidden = true;
      sessionHeading.setAttribute("aria-expanded", "false");
    }
  };
  // Item 2ni (c): read once, before anything rewrites the address.
  const openedFromSettings = new URLSearchParams(location.search).get("from") === "settings";
  const settings = node("button", "shell-settings");
  settings.type = "button";
  settings.textContent = "⚙";
  settings.setAttribute("aria-label", "Settings");
  settings.title = "Settings";
  new MutationObserver(applyPanel).observe(settings, { attributes: true, attributeFilter: ["aria-expanded"] });
  settings.addEventListener("click", () => {
    const closing = settings.getAttribute("aria-expanded") === "true";
    const navigation = { kind: "settings", from: closing ? "settings" : page, to: closing ? page : "settings", fullDocument: page !== "chat", chatID: store.active, mutationToken: store.mutation_token };
    if (page === "chat") beginNavigation(navigation);
    else requestNavigation(navigation, settings.dataset.target);
  });
  const windowControls = node("div", "shell-window-controls");
  windowControls.setAttribute("aria-hidden", "true");
  for (const kind of ["minimize", "maximize", "close"]) {
    const control = node("span", `shell-window-control ${kind}`);
    control.dataset.action = kind;
    control.addEventListener("click", () => { void api("/api/host-window", { action: kind }).catch((error) => report(error.message)); });
    const glyph = node("span", "shell-window-control-glyph");
    control.append(glyph);
    windowControls.append(control);
  }
  // Item 2px (f): the chat's connection lamp, from the one health state.
  const sessionLamp = node("span", "shell-session-lamp");
  right.append(sessionLamp, sessionHeading, connectionMenu, settings, windowControls);
  root.append(left, right);
  document.addEventListener("click", (event) => {
    for (const menu of document.querySelectorAll(".shell-menu")) {
      const anchor = menuAnchors.get(menu);
      if (!menu.hidden && !menu.contains(event.target) && !anchor?.contains(event.target)) menu.hidden = true;
    }
  });
  // Item 2gh: Escape dismisses the open menu and the arrow keys move through
  // its entries. Measured before the change, Escape did nothing and no key
  // reached the entries, although each one was already a focusable button.
  // Focus is not taken when the menu opens — that would move the operator's
  // focus for a menu he may only be reading — it is taken on the first arrow.
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" && event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const menu = [...document.querySelectorAll(".shell-menu")].find((value) => !value.hidden);
    if (!menu) {
		if (event.key === "Escape" && page !== "chat") {
			// Item 2ni (c): the same close the gear performs, so there is one way
			// back rather than 2gf's separate stand-in route.
			event.preventDefault();
			returnToChat();
		}
		return;
	}
    if (event.key === "Escape") {
      menu.hidden = true;
      event.preventDefault();
      return;
    }
    const rows = [...menu.querySelectorAll("button, a")].filter((row) => !row.disabled);
    if (!rows.length) return;
    const index = rows.indexOf(document.activeElement);
    const next = event.key === "ArrowDown"
      ? (index < 0 ? 0 : Math.min(rows.length - 1, index + 1))
      : (index < 0 ? rows.length - 1 : Math.max(0, index - 1));
    rows[next].focus();
    event.preventDefault();
  });

  function report(message) {
    if (options.reportError) options.reportError(message);
    else {
      root.dataset.error = message;
    }
  }

  function connectionState(connection) {
    return connectionHealth(store, connection.id).word;
  }

  function renderConnectionMenu() {
    const session = store.sessions[store.selection.session_id];
    const configured = configuredAgent(session);
    connectionMenu.replaceChildren();
    const profile = node("span", "shell-menu-empty");
    profile.textContent = `Profile · ${store.profiles?.active || store.config.profiles?.active || "unknown"}`;
    connectionMenu.append(profile);
    for (const connection of store.connections || store.config.connections || []) {
      const row = button("", `Use ${connection.label || connection.id}`, `shell-connection-choice ${configured?.b === connection.id ? "selected" : ""}`);
      let host = connection.base_url || "";
      try { host = new URL(host).host || host; } catch {}
      // Item 2mh (b), (d) and (e): THE SWITCHER SHOWS THE MODEL.
      //
      // It showed label, host and state, and at any width `server-2` identifies
      // nothing — the operator has to already know which box is which. The model
      // is what he is actually choosing between.
      //
      // (d) a llama.cpp model is a full GGUF path, so it is reduced to its
      // basename the same way the Connections page reduces it, with the whole
      // string kept on hover. (e) "model" is not a model: a connection that has
      // never been tested says so rather than naming one that does not exist.
      const rawModel = String(connection.model || "").trim();
      const named = rawModel && rawModel !== "model";
      const modelLabel = named ? rawModel.split(/[\/]/).pop() : "not tested";
      row.innerHTML = `<span>${escapeHTML(connection.label || connection.id)}</span>`
        + `<span class="shell-connection-model${named ? "" : " shell-connection-untested"}" title="${escapeHTML(named ? rawModel : "this connection has not been tested")}">${escapeHTML(modelLabel)}</span>`
        + `<span>${escapeHTML(host)}</span><span>${escapeHTML(connectionState(connection, session))}</span>`;
      row.onclick = async () => {
        const current = store.sessions[store.selection.session_id];
        if (isRunning(current)) {
          const refusal = node("span", "shell-menu-empty alarm");
          refusal.textContent = "stop the run first";
          connectionMenu.append(refusal);
          return;
        }
        try {
          const agentID = agentKey(configuredAgent(current));
          await api(`/api/agents/${encodeURIComponent(agentID)}/connection`, { action: "set", connection_id: connection.id });
          reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          connectionMenu.hidden = true;
          sessionHeading.setAttribute("aria-expanded", "false");
        } catch (error) { report(error.message); }
      };
      connectionMenu.append(row);
    }
  }

  function chatState(session) {
    if (session?.model_unreachable) return "offline";
    if (session?.pending_approval || session?.pending_repo_policy || session?.run?.status === "paused") return "waiting";
    if (activeRunStates.has(session?.run?.status)) return "running";
    return "idle";
  }

  // Item 2ni: THE PLAN IS NOT A TAB ANY MORE, so the strip's static-surface pass,
  // its plan chip, its right-click Hide and that Hide's confirmation are all gone with
  // it: "i decided i think i want it under settings, as its own top level item". The
  // strip is chats only again, as it was before item 2mf, and the Plan is a section in
  // the Settings nav. Hiding it is the switch on Settings > Chats, which now hides that
  // section; the surface, its URL and its page are untouched, exactly as 2mf (f) said.

  // Item 2gf: the one way back, used by the Plan toggle and by the stand-in
  // "Chat" route. It must work with the model unreachable, with `/api/plan`
  // failing and with no chat selected yet, so it never reads anything that a
  // failed fetch could have left empty: with no chat at all it goes to /chat,
  // which is the empty launch well.
  function returnToChat() {
    const selected = store.sessions?.[store.selection?.session_id];
    const open = Object.values(store.sessions || {}).filter((session) => !session.closed && session.role !== "c");
    const session = (selected && !selected.closed && selected.role !== "c") ? selected : open[0];
    if (!session) return void requestNavigation({ kind: "flip", from: page, to: "chat", fullDocument: true, chatID: "", mutationToken: store.mutation_token }, "/chat");
    openSide(`agent_${session.role === "d" ? "d" : "b"}`, session.id, "chat");
  }

  function openSide(agentID, sessionID, next) {
    const navigation = { kind: "flip", from: page, to: next, fullDocument: !options.switchView, chatID: sessionID, mutationToken: store.mutation_token };
    setSelection(agentID, sessionID);
    const suffix = sessionID ? `?session=${encodeURIComponent(sessionID)}` : "";
    if (options.switchView) options.switchView(next, navigation);
    else requestNavigation(navigation, next === "chat" ? `/chat${suffix}` : `/${suffix}`);
  }

  function configuredAgent(session) {
    return (store.config.agents || []).find((agent) => agentKey(agent) === session?.agent_id) || store.config.agents?.[0];
  }

  function showRoleMenu(menu, anchor, configured) {
    menu.replaceChildren();
    const name = configured?.name || "Agent";
    const chat = button(`agent_b · ${name} — chat`, "Open chat", "shell-new-choice");
    chat.onclick = () => { menu.hidden = true; void createChat("agent_b"); };
    const plan = button(`agent_d · ${name} — plan`, "Open plan chat", "shell-new-choice");
    plan.onclick = () => { menu.hidden = true; void createChat("agent_d", configured); };
    menu.append(chat, plan);
    revealMenu(menu, anchor);
  }

  function renderChatPanel() {
    if (page !== "chat") return;
    const configured = configuredAgent(store.sessions[store.selection.session_id]);
    const hasD = !!String(configured?.d || "").trim();
    setAttr(newChatButton, "title", hasD ? "New chat or plan" : "New chat with agent_b");
    setAttr(newChatButton, "aria-label", newChatButton.title);
    setProperty(newChatButton, "disabled", store.replay || !(store.config.agents || []).length);
    newChatButton.onclick = () => hasD ? showRoleMenu(newChatMenu, newChatButton, configured) : void createChat("agent_b");
    const arranged = arrangeChats(store.sessions, chatTree);
    chatList.replaceChildren();
    const act = async (body, state = false) => {
      try {
        chatTree = await api("/api/chats/tree", body);
        if (state) reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
        renderChatPanel();
      } catch (error) { report(error.message); }
    };
    const folderTargets = new Map();
    const folderHeading = (path, parent) => {
      const details = document.createElement("details"); details.className = "chat-list-folder"; details.dataset.folder = path;
      const heading = document.createElement("summary"); const label = node("span", "chat-list-folder-name"); label.textContent = path.split("/").at(-1); heading.append(label);
      const add = iconButton("folder-plus", `New folder in ${label.textContent}`, "chat-list-folder-action");
      add.onclick = (event) => { event.preventDefault(); const name = prompt("New folder name"); if (name) void act({ action: "add", parent: path, name }); };
      const rename = iconButton("pencil", `Rename ${label.textContent}`, "chat-list-folder-action");
      rename.onclick = (event) => { event.preventDefault(); const name = prompt("Folder name", label.textContent); if (name) void act({ action: "rename", path, name }); };
      const remove = button("×", `Delete ${path}`, "chat-list-folder-delete");
      remove.onclick = (event) => { event.preventDefault(); void act({ action: "delete", path }); };
      heading.append(add, rename, remove); details.append(heading);
      details.ondragover = (event) => event.preventDefault();
      details.ondrop = (event) => { event.preventDefault(); const id = event.dataTransfer.getData("text/plain"); if (id) void act({ action: "move", id, folder: path }); };
      (folderTargets.get(parent) || chatList).append(details); folderTargets.set(path, details);
      return details;
    };
    for (const group of arranged.folders) folderHeading(group.path, group.path.split("/").slice(0, -1).join("/"));

    const renderRow = (session, parent) => {
      const meta = (chatTree.chats || []).find((chat) => chat.id === session.id) || {};
      const row = node("div", `chat-list-row ${session.id === store.selection.session_id ? "selected" : ""}`); row.dataset.session = session.id;
      row.draggable = true; row.ondragstart = (event) => event.dataTransfer.setData("text/plain", session.id);
      const state = node("span", `chat-list-state ${chatState(session)}`); state.title = chatState(session); state.setAttribute("aria-label", chatState(session));
      const name = button(chatRowText(session), chatName(session), "chat-list-name");
      name.onclick = async () => {
        try {
          if (session.closed) await api(`/api/sessions/${encodeURIComponent(session.id)}/reopen`, {});
          if (session.closed) reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          setSelection(`agent_${session.role === "d" ? "d" : "b"}`, session.id);
          const settingsOpen = settings.getAttribute("aria-expanded") === "true";
          if (settingsOpen) document.dispatchEvent(new CustomEvent("settings.close", { detail: { surface: "chat", after: () => openSide(`agent_${session.role === "d" ? "d" : "b"}`, session.id, "chat") } }));
          else if (page !== "chat") openSide(`agent_${session.role === "d" ? "d" : "b"}`, session.id, "chat");
          else render();
        } catch (error) { report(error.message); }
      };
      const more = button("⋮", `${chatName(session)} menu`, "chat-list-more");
      const menu = node("div", "shell-menu chat-list-row-menu"); menu.hidden = true;
      more.onclick = (event) => {
        event.stopPropagation();
        for (const open of chatList.querySelectorAll(".chat-list-row-menu")) if (open !== menu) open.hidden = true;
        menu.replaceChildren();
        const labels = menuLabels(!!meta.pinned);
        const pin = button(labels[0], labels[0], "chat-list-menu-action"); pin.onclick = () => void act({ action: "pin", id: session.id, pinned: !meta.pinned });
        const rename = button(labels[1], labels[1], "chat-list-menu-action"); rename.onclick = () => { menu.hidden = true; showRename(row, session); };
        const move = button(labels[2], labels[2], "chat-list-menu-action");
        move.onclick = () => {
          let choices = menu.querySelector(".chat-list-move-choices");
          if (choices) return void choices.remove();
          choices = node("div", "chat-list-move-choices");
          for (const folder of ["", ...(chatTree.folders || [])]) { const choice = button(folder || "No folder", folder || "No folder", "chat-list-menu-action"); choice.onclick = () => void act({ action: "move", id: session.id, folder }); choices.append(choice); }
          menu.append(choices);
        };
        const remove = button(labels[3], labels[3], "chat-list-menu-action alarm"); remove.onclick = () => void deleteChat(session);
        menu.append(pin, rename, move, remove); menu.hidden = false; revealMenu(menu, more);
      };
      row.append(state, name, more, menu); parent.append(row);
    };
    if (arranged.pinned.length) { const group = node("section", "chat-list-group pinned"); const heading = node("strong", "chat-list-group-name"); heading.textContent = "Pinned"; group.append(heading); arranged.pinned.forEach((chat) => renderRow(chat, group)); chatList.prepend(group); }
    for (const group of arranged.folders) { const target = folderTargets.get(group.path); group.chats.forEach((chat) => renderRow(chat, target)); }
    const root = node("section", "chat-list-group root"); const rootHeading = node("strong", "chat-list-group-name"); rootHeading.textContent = "Chats";
    const add = iconButton("folder-plus", "New folder", "chat-list-folder-action"); add.onclick = () => { const name = prompt("New folder name"); if (name) void act({ action: "add", parent: "", name }); };
    rootHeading.append(add); root.append(rootHeading); root.ondragover = (event) => event.preventDefault(); root.ondrop = (event) => { event.preventDefault(); const id = event.dataTransfer.getData("text/plain"); if (id) void act({ action: "move", id, folder: "" }); };
    arranged.root.forEach((chat) => renderRow(chat, root)); chatList.append(root);
    const archived = chatTree.archived || [];
    if (archived.length) { const group = document.createElement("details"); group.className = "chat-list-archived"; const heading = document.createElement("summary"); heading.textContent = "Archived"; group.append(heading); for (const chat of archived) { const row = node("div", "chat-list-row"); const name = node("span", "chat-list-name"); name.textContent = chat.name; name.title = chat.name; const restore = button("Restore", `Restore ${chat.name}`, "chat-list-menu-action"); restore.onclick = () => void act({ action: "restore", id: chat.id }, true); row.append(name, restore); group.append(row); } chatList.append(group); }
    if (!chatTreeLoaded) void refreshChatTree();
  }

  function refreshChatTree() {
    if (chatTreeRefresh) return chatTreeRefresh;
    chatTreeLoaded = true;
    chatTreeRefresh = api("/api/chats/tree", undefined, "GET")
      .then((value) => { chatTree = value; renderChatPanel(); })
      .catch(() => { chatTreeLoaded = false; })
      .finally(() => { chatTreeRefresh = null; });
    return chatTreeRefresh;
  }

  function showRename(row, session) {
    const editor = node("form", "agent-chat-rename-form");
    const input = document.createElement("input");
    input.value = chatName(session);
    input.setAttribute("aria-label", "Chat name");
    const save = button("Save", "Save chat name", "agent-chat-rename-save");
    save.type = "submit";
    editor.append(input, save);
    editor.onsubmit = async (event) => {
      event.preventDefault();
      const label = input.value.trim();
      if (!label) return;
      try {
        await api(`/api/sessions/${encodeURIComponent(session.id)}`, { label });
        reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
        renderChatPanel();
      } catch (error) { report(error.message); }
    };
    row.replaceChildren(editor);
    input.focus();
    input.select();
  }

  const deleteConfirmText = "Delete this chat permanently? Memory notes it made are kept.";
  async function deleteChat(session) {
    if (!window.confirm(deleteConfirmText)) return;
    const wasSelected = store.selection.session_id === session.id;
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}`, undefined, "DELETE");
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      if (wasSelected) {
        const next = Object.values(store.sessions).filter((chat) => chat.id !== session.id && chat.role !== "c").sort((a, b) => Date.parse(b.last_activity || b.created_at || 0) - Date.parse(a.last_activity || a.created_at || 0))[0];
        setSelection(next ? `agent_${next.role === "d" ? "d" : "b"}` : "agent_b", next?.id || "");
      }
      renderChatPanel();
    } catch (error) { report(error.message); }
  }

  // Item 2gh: the menu opens AT THE POINTER when there is one, and at the
  // anchor otherwise (the `+` menu has no pointer of its own). Measured before
  // the change, a right-click 41 px into the tab put the menu's left edge 41 px
  // to the left of the pointer, because it was placed at the tab. It is still
  // clamped into the window, which the edge measurement confirms.
  function revealMenu(menu, anchor, point = null) {
    for (const open of document.querySelectorAll(".shell-menu")) if (open !== menu) open.hidden = true;
    menuAnchors.set(menu, anchor);
    menu.hidden = false;
    const anchorRect = anchor.getBoundingClientRect();
    const menuRect = menu.getBoundingClientRect();
    const left = point ? point.x : anchorRect.left;
    const top = point ? point.y : anchorRect.bottom;
    menu.style.left = `${Math.max(8, Math.min(left, innerWidth - menuRect.width - 8))}px`;
    menu.style.right = "auto";
    menu.style.top = `${Math.max(8, Math.min(top, innerHeight - menuRect.height - 8))}px`;
  }

  async function createChat(agentID = "agent_b", configured = null) {
    const source = store.sessions[store.selection.session_id] || Object.values(store.sessions).sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0))[0];
    try {
      const configuredID = agentKey(configured || configuredAgent(source));
      const body = agentID === "agent_d"
        ? { agent_id: configuredID, role: "d" }
        : source && source.role !== "d" ? { source_session_id: source.id } : { agent_id: configuredID };
      const result = await api("/api/sessions", body);
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      setSelection(agentID, result.session.id);
    } catch (error) { report(error.message); }
  }

  function render(renderPanel = true) {
    const session = store.sessions[store.selection.session_id];
    // Item 2gl (v1.2.6): the window's own title. The overlay could not be made
    // to activate - measured on Edge 153 under --app= and with no unattended
    // way to install the app - so the system strip stays, and the least it can
    // do is say which chat is in the window instead of naming the connection,
    // which the header beside the tab strip already says.
    document.title = session ? `Agent_b · ${chatName(session)}` : "Agent_b";
    const health = connectionHealth(store, session?.connection_id);
    // Kept in the layout whether shown or not, so the shell measures the same on
    // every surface (the chat and Settings over it).
    setProperty(sessionLamp.style, "visibility", session ? "" : "hidden");
    if (sessionLamp.dataset.state !== health.lamp) sessionLamp.dataset.state = health.lamp;
    setProperty(sessionLamp, "title", health.word);
	const notRunnableReason = String(session?.not_runnable_reason || "").trim();
    const heading = session ? (session.runnable === false ? (notRunnableReason || "Chat is not runnable") : sessionTitle(session)) : "No chat selected";
	const state = session ? (session.runnable === false ? "not_runnable" : "named") : "no_chat";
	const reason = state === "not_runnable" ? headerReasonCode(notRunnableReason) : "none";
	const telemetry = `${state}:${reason}`;
	if (telemetry !== headerTelemetry) {
		headerTelemetry = telemetry;
		void api("/api/header-state", { state, reason_code: reason }).catch(() => {});
	}
    if (sessionHeading.textContent !== heading) {
      sessionHeading.textContent = heading;
      // Item 2hc (v1.3.0/W7): the header is snapped to whole pixels.
      //
      // The strip's right-hand group is sized to its content, and the title's
      // own width is the text's natural width - 14.406 px, measured. That
      // fraction became the group's left edge (x = 1165.594), so the title's
      // text began on a fractional coordinate and was rasterised at a subpixel
      // phase. Two captures of one build then disagreed on about 300 px of that
      // text, at the same position, in roughly one pair of runs in two.
      //
      // Rounding the box UP to the next whole pixel puts the text origin, and
      // with it every item to its right, on integers. It is measured and set
      // only when the text changes, so an unchanged header costs no layout.
      sessionHeading.style.width = "";
      if (heading) {
        const natural = sessionHeading.getBoundingClientRect().width;
        if (natural > 0) sessionHeading.style.width = `${Math.ceil(natural)}px`;
      }
    }
    if (renderPanel) renderChatPanel();
    const query = new URLSearchParams();
    if (session) query.set("session", session.id);
    const suffix = query.size ? `?${query}` : "";
    const configured = configuredAgent(session);
    // Item 2hb: on a page that is its own document the gear is a LINK, and the
    // document it opens has no other way to know where it came from. The view
    // being left is named in the address, so closing can return to it.
    //
    // Item 2ni (c): AND A PAGE REACHED FROM THE SETTINGS NAV CLOSES LIKE SETTINGS.
    // The Plan is a Settings section now, and its page is its own document, so
    // arriving here means Settings navigated. The gear is that sheet's close: it
    // reads as closed-able and returns to the view the gear was clicked from,
    // which is the only way back the Plan needs now that its tab is gone.
    if (page !== "chat" && openedFromSettings) {
      setAttr(settings, "aria-expanded", "true");
      setAttr(settings, "aria-label", "Close settings");
      setAttr(settings, "title", "Close settings");
      setAttr(settings, "data-target", `/chat${suffix}`);
    } else {
      setAttr(settings, "data-target", `/chat${suffix}${suffix ? "&" : "?"}from=${page}#settings/connections`);
    }
    options.syncLocation?.(page);
  }

  subscribe((_state, event) => {
    if (event.type === "chat.list.patch") {
      void refreshChatTree();
      return;
    }
    // Stream text and reasoning patches redraw the transcript, never the
    // bounded chat list. A row redraw is needed only when one of its own
    // visible fields can have changed.
    if (event.type === "projection.patch") {
      const operations = event.data?.operations || [];
      const panelChanged = operations.some((operation) => /^\/(label|closed|last_activity|model_unreachable|pending_approval|pending_repo_policy|run\/status)$/.test(operation.path));
      render(panelChanged);
      return;
    }
    render();
  });
  return {
    render,
    report,
    setPage(next) {
      page = next;
      root.dataset.page = next;
      applyPanel();
      // Item 2mf (b): an in-page surface change moves the selection with it.
      const surface = surfaceForPage(next, store.selection.session_id);
      if (surface && surface.kind !== "chat") setSurface(surface);
      render();
    },
    newChat() {
      if (!store.replay) void createChat("agent_b");
    },
  };
}

function headerReasonCode(reason = "") {
	if (!reason) return "missing_reason";
	if (reason.includes("no connection")) return "no_connection";
	if (reason.includes("no longer exists")) return "missing_connection";
	if (reason.includes("folder is missing")) return "missing_workspace";
	if (reason.includes("folder is unavailable")) return "workspace_unavailable";
	if (reason.includes("base_url")) return "missing_endpoint";
	if (reason.includes("model")) return "missing_model";
	return "other";
}

function node(tag, className) {
  const value = document.createElement(tag);
  value.className = className;
  return value;
}
function button(text, title, className) {
  const value = node("button", className);
  value.type = "button";
  value.textContent = text;
  value.title = title;
  return value;
}
function iconButton(kind, title, className) {
  const value = button("", title, className);
  value.setAttribute("aria-label", title);
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("aria-hidden", "true");
  const paths = kind === "folder-plus"
    ? ["M3.5 6.5h6l2 2h9v10h-17z", "M12 11v5M9.5 13.5h5"]
	: kind === "archive" ? ["M4 7h16v13H4zM3 4h18v3H3zM9 11h6"]
	: kind === "restore" ? ["M5 8v-4m0 0h4M5 4l3 3M5.5 9a7 7 0 1 0 2-3"]
    : ["M5 19l3.5-.8L19 6.7 16.3 4 5.8 15.5z", "M14.8 5.5l2.7 2.7"];
  for (const shape of paths) {
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", shape);
    svg.append(path);
  }
  value.append(svg);
  return value;
}
function setAttr(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}
function setOptionalAttr(node, name, value) {
  if (value) setAttr(node, name, value);
  else if (node.hasAttribute(name)) node.removeAttribute(name);
}
function setProperty(node, name, value) {
  if (node[name] !== value) node[name] = value;
}
function reconcileNodes(parent, nodes) {
  const wanted = new Set(nodes);
  for (const child of [...parent.children]) if (!wanted.has(child)) child.remove();
  let cursor = parent.firstElementChild;
  for (const node of nodes) {
    if (node === cursor) cursor = cursor.nextElementSibling;
    else parent.insertBefore(node, cursor);
  }
}
function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);
}
