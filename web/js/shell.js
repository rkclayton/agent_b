import { api, reduce, setSelection, setSurface, store, subscribe } from "./bus.js";
import { chatName, chatRowText, isRunning, sessionTitle } from "./chat-lifecycle.js";
import { installUIErrorRelay } from "./ui-error-relay.js";
import { installPageHealth } from "./page-health.js";
import { requestNavigation } from "./navigation-guard.js";
import { surfaceForPage } from "./surfaces.js";
import { connectionHealth } from "./settings-connections.js";
import { beginNavigation } from "./navigation-telemetry.js";

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
  const newChatButton = button("+", "New chat with agent_b", "agent-tab-new");
  const newChatMenu = node("div", "shell-menu shell-new-menu");
  newChatMenu.hidden = true;
  const tabs = node("nav", "agent-tabs");
  tabs.setAttribute("aria-label", "Chats");
  const tabViews = new Map();
  let chatTree = { folders: [], chats: [] };
  left.append(newChatButton, newChatMenu, tabs);

  const right = node("div", "shell-right");
  const sessionHeading = button("", "Switch model", "shell-session-title");
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
    if (!root.contains(event.target)) for (const menu of root.querySelectorAll(".shell-menu")) menu.hidden = true;
  });
  // Item 2gh: Escape dismisses the open menu and the arrow keys move through
  // its entries. Measured before the change, Escape did nothing and no key
  // reached the entries, although each one was already a focusable button.
  // Focus is not taken when the menu opens — that would move the operator's
  // focus for a menu he may only be reading — it is taken on the first arrow.
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" && event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const menu = [...root.querySelectorAll(".shell-menu")].find((value) => !value.hidden);
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

  function sessionsFor(agentID, includeClosed = true) {
    return Object.values(store.sessions)
      // Item 2lu (b): ONE answer, not two — the menu and the tab strip above use
      // the same rule, so a session that gets a tab also gets a row in it.
      .filter((session) => (store.replay || session.role !== "c") && `agent_${session.role === "d" ? "d" : "b"}` === agentID && (includeClosed || !session.closed))
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0));
  }

  function agentName(agentID) {
    const selected = store.sessions[store.selection.session_id];
    if (agentID === "agent_b") {
      if (selected?.agent_name) return selected.agent_name;
    }
	const selectedAgentID = selected?.agent_id || agentKey(store.config.agents?.[0]);
	const configured = (store.config.agents || []).find((agent) => agentKey(agent) === selectedAgentID) || store.config.agents?.[0];
	const connectionID = configured?.[agentID.replace("agent_", "")];
	const connection = (store.connections || []).find((item) => item.id === connectionID);
    return connection?.label || connectionID || agentID;
  }

  function agentState(agentID) {
    const sessions = sessionsFor(agentID, false);
    if (sessions.some((item) => item.model_unreachable)) return "offline";
    if (sessions.some((item) => item.pending_approval || item.pending_repo_policy || item.run?.status === "paused")) return "waiting";
    if (sessions.some((item) => activeRunStates.has(item.run?.status))) return "running";
    return "idle";
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

  function renderTabs() {
    // Item 2eo: a live run sends a steady stream of patches, and rebuilding the
    // strip replaced an open tab menu with a hidden one, so right-click did
    // nothing while the model was thinking. The strip still rebuilds; a menu
    // that is open moves, as the same element, into its tab's new wrap, so the
    // entry being pointed at is never replaced underneath the pointer.
    // A worker has no chat: role c never appears in the tab strip.
    // Item 2gn: a closed chat the operator opened by name gets a tab, so the
    // chat he is looking at is the one the strip shows selected. Without it he
    // read a transcript with no tab of its own and the strip said he was
    // somewhere else. Only the selected one appears; the rest of the closed
    // history stays in the tab menu where it lives.
    // Item 2lu (a): a session the page is DISPLAYING is listed by the tab that
    // is displaying it, whatever route it arrived by. A c-role session is a
    // worker chat and is deliberately not listed beside the operator's own
    // chats — but IN REPLAY there is no such distinction to preserve: there is
    // only what was replayed, and the page is showing it. rel-1.19.0/W4
    // measured a replayed session as role=c agent_id=acceptance, so the
    // premise 2lu was written on — "no agent binding the page can see" — was
    // not what was happening; the binding was there and the filter was hiding
    // it from every tab.
    const open = Object.values(store.sessions)
      .filter((session) => (store.replay || session.role !== "c") && (!session.closed || session.id === store.selection.session_id))
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0));
    const selectedSession = store.sessions[store.selection.session_id];
    const configured = configuredAgent(selectedSession);
    const hasD = !!String(configured?.d || "").trim();
    setAttr(newChatButton, "title", hasD ? "New chat or plan" : "New chat with agent_b");
    setAttr(newChatButton, "aria-label", newChatButton.title);
    setProperty(newChatButton, "disabled", store.replay || !(store.config.agents || []).length);
    newChatButton.onclick = () => hasD ? showRoleMenu(newChatMenu, newChatButton, configured) : void createChat("agent_b");
    const rendered = open.length ? open : [null];
    const nodes = [];
    const used = new Set();
    for (const session of rendered) {
      const agentID = `agent_${session?.role === "d" ? "d" : "b"}`;
      const key = session?.id || agentID;
      used.add(key);
      let view = tabViews.get(key);
      if (!view) {
        const wrap = node("div", "agent-tab-wrap");
        const tab = button("", "", "agent-tab");
        const robot = node("span", `agent-tab-robot agent-tab-robot-${agentID.slice(-1)}`);
        robot.setAttribute("aria-hidden", "true");
        robot.title = agentID;
        const image = document.createElement("img"); image.src = "/static/assets/agent.svg"; image.alt = "";
        robot.append(image, node("span", "agent-tab-eyes"));
        const nameNode = node("span", "agent-tab-name");
        tab.append(robot, nameNode);
        const menu = node("div", "shell-menu agent-chat-menu"); menu.hidden = true;
        wrap.append(tab, menu);
        view = { wrap, tab, robot, nameNode, menu, close: null };
        tabViews.set(key, view);
      }
      const { wrap, tab, robot, nameNode, menu } = view;
      if (menu.hidden && menu.childElementCount) menu.replaceChildren();
      setAttr(wrap, "data-agent", agentID);
      setOptionalAttr(wrap, "data-session", session?.id);
      const selected = !!session && store.selection.session_id === session.id;
      wrap.classList.toggle("selected", selected);
      // Item 2go: the tab carries the chat's NAME. The role is on the robot
      // glyph and in its hover text, which is where it was always readable; a
      // tab that says agent_b tells the operator nothing about the chat.
      const name = session ? chatName(session) : agentName(agentID);
      const glyphState = session ? chatState(session) : agentState(agentID);
      setAttr(tab, "class", `agent-tab ${selected ? "selected" : ""}`);
      setAttr(tab, "title", name);
      setAttr(tab, "data-agent", agentID);
      setOptionalAttr(tab, "data-session", session?.id);
      setAttr(tab, "aria-label", `${name} · ${agentID}`);
      setAttr(robot, "class", `agent-tab-robot agent-tab-robot-${agentID.slice(-1)} ${glyphState}`);
      if (nameNode.textContent !== name) nameNode.textContent = name;
      // Left click selects the chat. From any surface that is NOT this chat -
      // Settings, Plan, any later page - it also shows it, which is what 2gf
      // asked for: the Plan page had no way back at all, its tab click only
      // changed the selection and the window stayed where it was. Repeated
      // clicks never leave the chat, which is what 2ak objected to in the old
      // second-click flip. Item 2gk removed the second side the flip went to.
      tab.onclick = () => {
        if (!session) return;
        setSelection(agentID, session.id);
        const settingsOpen = document.querySelector(".shell-settings")?.getAttribute("aria-expanded") === "true";
        if (settingsOpen) {
          // Settings is not the chat, and item 2gf asks that one click
          // from it reaches the chat. The ⚙ toggle still closes Settings onto
          // the surface beneath.
			document.dispatchEvent(new CustomEvent("settings.close", { detail: {
				surface: "chat", after: () => openSide(agentID, session.id, "chat"),
			} }));
          return;
        }
        // On the chat the tab selects and does nothing else: you are already
        // there. Everywhere else it shows the chat.
        if (page !== "chat") openSide(agentID, session.id, "chat");
      };
      tab.oncontextmenu = (event) => {
        event.preventDefault();
        for (const other of tabs.querySelectorAll(".shell-menu")) if (other !== menu) other.hidden = true;
        // Item 2gh: a second right-click on the same tab dismisses it. Measured
        // before the change, it re-rendered and left the menu open.
        if (!menu.hidden) { menu.hidden = true; return; }
        renderAgentMenu(menu, agentID);
        revealMenu(menu, tab, { x: event.clientX, y: event.clientY });
      };
      if (session) {
        // The close mark overlays the tab's own trailing edge rather than sitting
        // beside it, so the tab's width is its label's width. It stays a sibling
        // of the tab because a button inside a button is not valid HTML.
        if (!view.close) {
          view.close = button("×", "", "agent-tab-close");
          wrap.insertBefore(view.close, menu);
        }
        setAttr(view.close, "title", `Close ${name}`);
        setAttr(view.close, "aria-label", `Close ${name}`);
        setProperty(view.close, "disabled", store.replay || isRunning(session));
        view.close.onclick = (event) => { event.stopPropagation(); void closeChat(session, menu, agentID); };
      } else if (view.close) {
        view.close.remove();
        view.close = null;
      }
      nodes.push(wrap);
    }
    reconcileNodes(tabs, nodes);
    for (const key of tabViews.keys()) if (!used.has(key)) tabViews.delete(key);
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

  function renderAgentMenu(menu, agentID, refresh = true) {
    const sessions = sessionsFor(agentID, true);
    const sourceID = menu.closest(".agent-tab-wrap")?.dataset.session || "";
    menu.replaceChildren();
	const tree = chatTree;
	const act = async (body) => { try { chatTree = await api("/api/chats/tree", body); renderAgentMenu(menu, agentID, false); } catch (error) { report(error.message); } };
	const root = node("div", "agent-chat-folder-root");
	const rootName = node("strong", ""); rootName.textContent = "chats"; root.append(rootName);
	const add = iconButton("folder-plus", "New folder in chats", "agent-chat-folder-action");
	add.onclick = () => { const name = prompt("New folder name"); if (name) void act({ action: "add", parent: "", name }); };
	root.append(add); menu.append(root);
	root.ondragover = (event) => event.preventDefault();
	root.ondrop = (event) => { event.preventDefault(); const id = event.dataTransfer.getData("text/plain"); if (id) void act({ action: "move", id, folder: "" }); };
	const targets = new Map([["", menu]]);
	const folders = [...(tree.folders || [])].sort((left, right) => left.split("/").length - right.split("/").length || left.localeCompare(right));
	for (const path of folders) {
		const details = document.createElement("details"); details.className = "agent-chat-folder";
		details.dataset.folder = path;
		const name = path.split("/").at(-1), parent = path.split("/").slice(0, -1).join("/");
		const heading = document.createElement("summary"); const folderName = node("span", ""); folderName.textContent = name; heading.append(folderName);
		const add = iconButton("folder-plus", `New folder in ${name}`, "agent-chat-folder-action");
		add.onclick = (event) => { event.preventDefault(); const child = prompt("New folder name"); if (child) void act({ action: "add", parent: path, name: child }); };
		const rename = iconButton("pencil", `Rename ${name}`, "agent-chat-folder-action");
		rename.onclick = (event) => { event.preventDefault(); const name = prompt("Folder name", path.split("/").at(-1)); if (name) void act({ action: "rename", path, name }); };
		const remove = button("×", `Delete ${path}`, "agent-chat-delete");
		remove.onclick = (event) => { event.preventDefault(); void act({ action: "delete", path }); };
		heading.append(add, rename, remove); details.append(heading); (targets.get(parent) || menu).append(details); targets.set(path, details);
		details.ondragover = (event) => event.preventDefault();
		details.ondrop = (event) => { event.preventDefault(); const id = event.dataTransfer.getData("text/plain"); if (id) void act({ action: "move", id, folder: path }); };
	}
    // Item 2go, the operator: "i want the chat summary removed from the top of
    // chats... i want it to display like this: MM:DD · Chat name · × , nothing
    // more." So there is no summary line, and the row below carries nothing
    // else either.
    if (!sessions.length) {
      const empty = node("span", "shell-menu-empty");
      // Item 2lu (c): a session the page holds but this tab cannot claim is a
      // STATE, and the words say which. "No chats" beside a visible transcript
      // is what sent rel-1.18.0 looking for a selector defect for an hour.
      const unclaimed = Object.values(store.sessions || {}).filter((session) => session.role === "c");
      empty.textContent = unclaimed.length
        ? `No chats for this agent — ${unclaimed.length} worker chat${unclaimed.length === 1 ? "" : "s"} elsewhere`
        : "No chats";
      menu.append(empty);
      return;
    }
    for (const session of sessions) {
      const row = node("div", `agent-chat-row ${session.closed ? "closed" : "open"} ${session.id === sourceID ? "selected" : ""}`);
      row.dataset.session = session.id;
	  row.draggable = true; row.ondragstart = (event) => event.dataTransfer.setData("text/plain", session.id);
      const summary = node("span", "agent-chat-summary");
      summary.textContent = chatRowText(session);
      // The full name on hover, because the row is one line and a long name
      // ends in an ellipsis (2go).
      summary.title = chatName(session);
      // Item 2oh: a choice replaces the chat in the tab that opened this menu.
      // Close that source, reopen the choice when needed, then select it.
      summary.onclick = async () => {
        const previous = store.sessions[sourceID];
        if (previous?.id !== session.id && isRunning(previous)) return report("This chat has a running run. Stop it before switching the tab.");
        try {
          if (previous?.id !== session.id) await api(`/api/sessions/${encodeURIComponent(previous.id)}/close`, {});
          if (session.closed) await api(`/api/sessions/${encodeURIComponent(session.id)}/reopen`, {});
          reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          menu.hidden = true;
          openSide(agentID, session.id, "chat");
        } catch (error) { report(error.message); }
      };
      const rename = iconButton("pencil", `Rename ${chatName(session)}`, "agent-chat-rename");
      rename.onclick = (event) => { event.stopPropagation(); showRename(row, session, menu, agentID); };
      const remove = button("×", `Delete ${chatName(session)}`, "agent-chat-delete");
      remove.onclick = () => void deleteChat(session, menu, agentID);
      // Item 2py (f): Delete works whatever the chat is doing; the server stops and
      // closes it first.
      remove.disabled = store.replay;
      row.append(summary, rename, remove);
	  const chat = (tree.chats || []).find((item) => item.id === session.id);
	  (targets.get(chat?.folder || "") || menu).append(row);
    }
	if (refresh) void api("/api/chats/tree", undefined, "GET").then((value) => { chatTree = value; renderAgentMenu(menu, agentID, false); }).catch(() => {});
  }

  function showRename(row, session, menu, agentID) {
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
        renderAgentMenu(menu, agentID);
      } catch (error) { report(error.message); }
    };
    row.replaceChildren(editor);
    input.focus();
    input.select();
  }

  // Item 2hq (v1.6.2): close only moves the chat out of the open tabs. Delete is
  // the intentional, confirmed act, and since 2py it leaves no copy of the chat.
  const deleteConfirmText = "Delete this chat permanently? Memory notes it made are kept.";

  async function closeChat(session, menu, agentID) {
    if (isRunning(session)) return report("This chat has a running run. Stop it before closing the chat.");
    // Item 2ms (a) and (b): WHICH CHAT THE WINDOW SHOWS AFTER A CLOSE.
    //
    // Reproduced against a copy of the operator's own restored journal set, 34
    // chats: closing the one open chat left the selection pointing at it, and
    // because [[2hq]]'s close-is-not-delete keeps the session in the store with its
    // whole transcript, the pane went on rendering a chat he had just closed — and
    // a reload showed it again. "when no chat tabs are open its showing me an old
    // chat still in the window."
    //
    // Closing the SELECTED chat moves the selection to its neighbour in strip
    // order; closing any other chat moves nothing.
    const wasSelected = store.selection.session_id === session.id;
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/close`, {});
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      if (wasSelected) selectAfterClose(session, agentID);
      renderAgentMenu(menu, agentID);
    } catch (error) { report(error.message); }
  }

  // The neighbour that took its place: the nearest OPEN chat of the same agent in
  // the order the strip draws, and when there is none, nothing — which is the empty
  // launch well the shell already has, with no tab lit and the composer disabled.
  function selectAfterClose(closed, agentID) {
    const order = Object.values(store.sessions)
      .filter((one) => one && !one.closed && one.id !== closed.id && (store.replay || one.role !== "c"))
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0));
    const sameAgent = order.filter((one) => `agent_${one.role === "d" ? "d" : "b"}` === agentID);
    const next = sameAgent[0] || order[0] || null;
    if (!next) {
      setSelection(agentID, "");
      render();
      return;
    }
    setSelection(`agent_${next.role === "d" ? "d" : "b"}`, next.id);
    render();
  }

  // Item 2ms (e): A RELOAD WITH NO OPEN CHAT LANDS IN THE EMPTY STATE. The selection
  // is kept in sessionStorage, so a reload restored the closed chat it named and the
  // pane drew the transcript again. A closed chat stays selectable — that is
  // [[2gn]]'s open-by-name — so the address asking for it by name is honoured and
  // anything else is cleared.
  function clearClosedSelectionOnce() {
    if (!store.loaded || clearedClosedSelection) return;
    clearedClosedSelection = true;
    const selected = store.sessions[store.selection.session_id];
    if (!selected || !selected.closed) return;
    if (new URLSearchParams(location.search).get("session") === selected.id) return;
    setSelection(store.selection.agent_id, "");
  }

  async function deleteChat(session, menu, agentID) {
    if (!window.confirm(deleteConfirmText)) return;
    const wasSelected = store.selection.session_id === session.id;
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}`, undefined, "DELETE");
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      if (wasSelected) selectAfterClose(session, agentID);
      renderAgentMenu(menu, agentID);
    } catch (error) { report(error.message); }
  }

  // Item 2gh: the menu opens AT THE POINTER when there is one, and at the
  // anchor otherwise (the `+` menu has no pointer of its own). Measured before
  // the change, a right-click 41 px into the tab put the menu's left edge 41 px
  // to the left of the pointer, because it was placed at the tab. It is still
  // clamped into the window, which the edge measurement confirms.
  function revealMenu(menu, anchor, point = null) {
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

  function render() {
    const session = store.sessions[store.selection.session_id];
    // Item 2gl (v1.2.6): the window's own title. The overlay could not be made
    // to activate - measured on Edge 153 under --app= and with no unattended
    // way to install the app - so the system strip stays, and the least it can
    // do is say which chat is in the window instead of naming the connection,
    // which the header beside the tab strip already says.
    document.title = session ? `Agent_b · ${chatName(session)}` : "Agent_b";
    setProperty(sessionHeading, "hidden", !session);
    const health = connectionHealth(store, session?.connection_id);
    // Kept in the layout whether shown or not, so the shell measures the same on
    // every surface (the chat and Settings over it).
    setProperty(sessionLamp.style, "visibility", session ? "" : "hidden");
    if (sessionLamp.dataset.state !== health.lamp) sessionLamp.dataset.state = health.lamp;
    setProperty(sessionLamp, "title", health.word);
    const heading = session ? (session.runnable === false ? session.not_runnable_reason : sessionTitle(session)) : "";
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
    renderTabs();
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

  let clearedClosedSelection = false;
  subscribe((_state, event) => {
    clearClosedSelectionOnce();
    render();
  });
  return {
    render,
    report,
    setPage(next) {
      page = next;
      root.dataset.page = next;
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
