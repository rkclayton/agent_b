import { api, reduce, setSelection, setSurface, store, subscribe } from "./bus.js";
import { chatName, chatRowText, isRunning, sessionTitle } from "./chat-lifecycle.js";
import { installUIErrorRelay } from "./ui-error-relay.js";
import { requestNavigation } from "./navigation-guard.js";
import { canHide, surfaceForPage, surfaceHref, surfaceLabel, surfaceTitle, visibleStaticSurfaces, withHidden } from "./surfaces.js";
import { beginNavigation } from "./navigation-telemetry.js";

const activeRunStates = new Set(["running", "queued", "stopping"]);
const agentKey = (agent) => String(agent?.name || "").trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
// Item 2gk (v1.2.3): a chat had two sides and the shell remembered which one
// you were last on. There is one side now, so there is nothing to remember and
// nothing to flip to.

export function initShell(options = {}) {
	installUIErrorRelay({ token: () => store.mutation_token, sessionID: () => store.active });
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
  right.append(sessionHeading, connectionMenu, settings, windowControls);
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

  function connectionState(connection, session) {
    if (session?.connection_id === connection.id && session.model_unreachable) return "offline";
    if ((connection.capabilities?.findings || []).some((line) => String(line).startsWith("probe failed:"))) return "offline";
    return connection.capabilities?.probed_at ? "ready" : "not tested";
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
    const openMenus = new Map();
    for (const wrap of tabs.querySelectorAll(".agent-tab-wrap")) {
      const open = wrap.querySelector(":scope > .agent-chat-menu");
      if (open && !open.hidden) openMenus.set(wrap.dataset.session || wrap.dataset.agent, open);
    }
    tabs.replaceChildren();
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
    newChatButton.title = hasD ? "New chat or plan" : "New chat with agent_b";
    newChatButton.setAttribute("aria-label", newChatButton.title);
    newChatButton.disabled = store.replay || !(store.config.agents || []).length;
    newChatButton.onclick = () => hasD ? showRoleMenu(newChatMenu, newChatButton, configured) : void createChat("agent_b");
    const rendered = open.length ? open : [null];
    for (const session of rendered) {
      const agentID = `agent_${session?.role === "d" ? "d" : "b"}`;
      const wrap = node("div", "agent-tab-wrap");
      wrap.dataset.agent = agentID;
      if (session) wrap.dataset.session = session.id;
      const selected = !!session && store.selection.session_id === session.id;
      if (selected) wrap.classList.add("selected");
      // Item 2go: the tab carries the chat's NAME. The role is on the robot
      // glyph and in its hover text, which is where it was always readable; a
      // tab that says agent_b tells the operator nothing about the chat.
      const name = session ? chatName(session) : agentName(agentID);
      const tab = button("", name, `agent-tab ${selected ? "selected" : ""}`);
      const glyphState = session ? chatState(session) : agentState(agentID);
      tab.dataset.agent = agentID;
      if (session) tab.dataset.session = session.id;
      tab.setAttribute("aria-label", `${name} · ${agentID}`);
      const robot = agentID.slice(-1);
      tab.innerHTML = `<span class="agent-tab-robot agent-tab-robot-${robot} ${glyphState}" aria-hidden="true" title="${escapeHTML(agentID)}"><img src="/static/assets/agent.svg" alt=""><span class="agent-tab-eyes"></span></span><span class="agent-tab-name">${escapeHTML(name)}</span>`;
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
      const kept = openMenus.get(session ? session.id : agentID);
      const menu = kept || node("div", "shell-menu agent-chat-menu");
      if (!kept) menu.hidden = true;
      tab.oncontextmenu = (event) => {
        event.preventDefault();
        for (const other of tabs.querySelectorAll(".shell-menu")) if (other !== menu) other.hidden = true;
        // Item 2gh: a second right-click on the same tab dismisses it. Measured
        // before the change, it re-rendered and left the menu open.
        if (!menu.hidden) { menu.hidden = true; return; }
        renderAgentMenu(menu, agentID);
        revealMenu(menu, tab, { x: event.clientX, y: event.clientY });
      };
      wrap.append(tab);
      if (session) {
        // The close mark overlays the tab's own trailing edge rather than sitting
        // beside it, so the tab's width is its label's width. It stays a sibling
        // of the tab because a button inside a button is not valid HTML.
        const close = button("×", `Close ${name}`, "agent-tab-close");
        close.disabled = store.replay || isRunning(session);
        close.onclick = (event) => { event.stopPropagation(); void closeChat(session, menu, agentID); };
        wrap.append(close);
      }
      wrap.append(menu);
      tabs.append(wrap);
    }
    renderStaticSurfaces(selectedSession, configured);
  }

  // Item 2mf (c) and (g): PLAN IS A SURFACE AND IT LIVES IN THE STRIP, pinned at
  // the far right AFTER every chat, so it does not sort with them and does not
  // move when one opens, closes or reorders. The one-entry page nav it replaces
  // is gone, and so is its class.
  //
  // (g) THE STRIP STAYS A CHAT STRIP TO LOOK AT: this tab carries no robot glyph
  // and no run state, because it has neither, and it says so in its class rather
  // than by drawing a grey robot that would read as an idle conversation.
  function renderStaticSurfaces(selectedSession, configured) {
    for (const surface of visibleStaticSurfaces(store.config)) {
      // The availability rule the pages nav carried, moved with the surface and
      // not widened: the Plan is offered on a d-session, or when no separate d
      // connection is configured at all.
      if (surface.kind === "plan" && (!selectedSession || (selectedSession.role !== "d" && !!String(configured?.d || "").trim()))) continue;
      const label = surfaceLabel(surface);
      const wrap = node("div", "agent-tab-wrap agent-tab-wrap-surface");
      wrap.dataset.surfaceKind = surface.kind;
      wrap.dataset.surfaceKey = surface.key;
      const selected = page === surface.kind;
      if (selected) wrap.classList.add("selected");
      const tab = button("", surfaceTitle(surface), `agent-tab agent-tab-surface ${selected ? "selected" : ""}`);
      tab.dataset.surfaceKind = surface.kind;
      tab.dataset.surfaceKey = surface.key;
      tab.setAttribute("aria-label", `${surfaceTitle(surface)} · surface`);
      if (selected) tab.setAttribute("aria-current", "page");
      // [[2le]] and [[2lm]]: THE OPERATOR'S OWN PREPARED ARTWORK STAYS. It moved
      // with the surface out of the pages nav and is drawn at the same 12px from
      // the same three files, because the product runs at more than one device
      // pixel ratio and one asset cannot be sharp at 1x, 2x and 3x. (c) asked
      // for no ROBOT GLYPH and no run state, which this is not and does not have.
      tab.innerHTML = `${surfaceChip(surface)}<span class="agent-tab-name">${escapeHTML(label)}</span>`;
      tab.onclick = () => {
        // Item 2gf: from the surface itself the tab is the way back, exactly as
        // the pages nav's selected state was. From anywhere else it opens it.
        if (selected) return void returnToChat();
        setSurface(surface);
        const sessionID = store.selection.session_id || "";
        requestNavigation({ kind: "flip", from: page, to: surface.kind, fullDocument: true, chatID: sessionID, mutationToken: store.mutation_token }, surfaceHref(surface, sessionID));
      };
      const menu = node("div", "shell-menu agent-chat-menu");
      menu.hidden = true;
      tab.oncontextmenu = (event) => {
        event.preventDefault();
        for (const other of tabs.querySelectorAll(".shell-menu")) if (other !== menu) other.hidden = true;
        // Item 2gh: a second right-click on the same tab dismisses it.
        if (!menu.hidden) { menu.hidden = true; return; }
        renderSurfaceMenu(menu, surface, tab);
        revealMenu(menu, tab, { x: event.clientX, y: event.clientY });
      };
      wrap.append(tab, menu);
      tabs.append(wrap);
    }
  }

  function surfaceChip(surface) {
    if (surface.kind !== "plan") return "";
    return '<img class="shell-page-chip" src="/static/assets/plan-mark-nav.png" srcset="/static/assets/plan-mark-nav.png 1x, /static/assets/plan-mark-nav@2x.png 2x, /static/assets/plan-mark-nav@3x.png 3x" width="12" height="12" alt="" decoding="async">';
  }

  // (d): right-click offers Hide, through the context menu the strip already has,
  // and hiding ASKS FIRST — in [[2l4]]'s shape, the one anchored confirmation this
  // product uses, rather than a second confirm pattern invented here.
  function renderSurfaceMenu(menu, surface, anchor) {
    menu.replaceChildren();
    if (!canHide(surface)) return;
    const hide = button(`Hide ${surfaceTitle(surface)}`, `Hide the ${surfaceTitle(surface)} tab`, "shell-new-choice");
    hide.onclick = () => {
      menu.hidden = true;
      confirmHide(surface, anchor);
    };
    menu.append(hide);
  }

  function confirmHide(surface, anchor) {
    for (const stale of tabs.querySelectorAll(".confirm-popover")) stale.remove();
    const popover = node("div", "confirm-popover");
    const text = node("p", "");
    text.textContent = `Hide the ${surfaceTitle(surface)} tab? You can turn it back on in Settings.`;
    const actions = node("div", "confirm-actions");
    const cancel = button("Cancel", "Leave it where it is", "");
    const confirm = button("Hide", `Hide ${surfaceTitle(surface)}`, "default");
    cancel.onclick = () => popover.remove();
    confirm.onclick = async () => {
      popover.remove();
      try {
        // (f): the surface is not discarded. Only its name is remembered as
        // hidden, so re-enabling puts it back where the list puts it.
        await api("/api/config", { chat: { hidden_surfaces: withHidden(store.config, surface, true) } });
        reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      } catch (error) { report(error.message); }
    };
    actions.append(cancel, confirm);
    popover.append(text, actions);
    tabs.append(popover);
    revealMenu(popover, anchor);
    const dismiss = (event) => {
      if (popover.contains(event.target)) return;
      popover.remove();
      document.removeEventListener("pointerdown", dismiss, true);
    };
    document.addEventListener("pointerdown", dismiss, true);
    // Item 2l4 (c): Escape answers the popover first, and cancels it.
    popover.addEventListener("keydown", (event) => { if (event.key === "Escape") popover.remove(); });
    cancel.focus();
  }

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

  function renderAgentMenu(menu, agentID) {
    const sessions = sessionsFor(agentID, true);
    menu.replaceChildren();
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
      const row = node("div", `agent-chat-row ${session.closed ? "closed" : "open"}`);
      row.dataset.session = session.id;
      const summary = node("span", "agent-chat-summary");
      summary.textContent = chatRowText(session);
      // The full name on hover, because the row is one line and a long name
      // ends in an ellipsis (2go).
      summary.title = chatName(session);
      // Item 2gx: the row IS the control. One click makes the tab that was
      // right-clicked show this chat - it does not open a second tab and it
      // does not change which tab is selected out from under the pointer.
      summary.onclick = async () => {
        if (session.closed) {
          try {
            await api(`/api/sessions/${encodeURIComponent(session.id)}/reopen`, {});
            reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          } catch (error) { return report(error.message); }
        }
        menu.hidden = true;
        openSide(agentID, session.id, "chat");
      };
      const rename = button("Rename", `Rename ${chatName(session)}`, "agent-chat-rename");
      rename.onclick = () => showRename(row, session, menu, agentID);
      const remove = button("Delete", `Delete ${chatName(session)}`, "agent-chat-delete");
      remove.onclick = () => void deleteChat(session, menu, agentID);
      const close = button("×", `Close ${chatName(session)}`, "agent-chat-close");
      close.disabled = session.closed || store.replay;
      close.onclick = () => void closeChat(session, menu, agentID);
      row.append(summary, rename);
      if (session.closed) row.append(remove);
      row.append(close);
      menu.append(row);
    }
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

  // Item 2hq (v1.6.2): close only moves the chat out of the open tabs. Delete
  // is the intentional, confirmed act available on an already-closed row.
  const deleteConfirmText = "Delete this chat? Its memory notes, plans and files stay.";

  async function closeChat(session, menu, agentID) {
    if (isRunning(session)) return report("This chat has a running run. Stop it before closing the chat.");
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}/close`, {});
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
      renderAgentMenu(menu, agentID);
    } catch (error) { report(error.message); }
  }

  async function deleteChat(session, menu, agentID) {
    if (!session.closed) return report("Close this chat before deleting it.");
    if (!window.confirm(deleteConfirmText)) return;
    try {
      await api(`/api/sessions/${encodeURIComponent(session.id)}`, undefined, "DELETE");
      reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
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
    sessionHeading.hidden = !session;
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
    settings.dataset.target = `/chat${suffix}${suffix ? "&" : "?"}from=${page}#settings/connections`;
    options.syncLocation?.(page);
  }

  subscribe((_state, event) => {
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
function escapeHTML(value) {
  return String(value).replace(/[&<>"']/g, (character) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[character]);
}
