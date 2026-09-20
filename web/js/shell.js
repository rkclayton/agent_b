import { api, reduce, setSelection, store, subscribe } from "./bus.js";
import { chatRowText, firstUserLine, isRunning, sessionTitle } from "./chat-lifecycle.js";
import { installUIErrorRelay } from "./ui-error-relay.js";
import { requestNavigation } from "./navigation-guard.js";
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

  const left = node("div", "shell-left");
  const newChatButton = button("+", "New chat with agent_b", "agent-tab-new");
  const newChatMenu = node("div", "shell-menu shell-new-menu");
  newChatMenu.hidden = true;
  const tabs = node("nav", "agent-tabs");
  tabs.setAttribute("aria-label", "Chats");
  left.append(newChatButton, newChatMenu, tabs);

  const right = node("div", "shell-right");
  const sessionHeading = node("span", "shell-session-title");
  const pages = node("nav", "shell-pages");
  pages.setAttribute("aria-label", "Pages");
  for (const [id, path] of [["plan", "/plan"]]) {
    const link = node("a", `shell-page ${page === id ? "selected" : ""}`);
    link.dataset.page = id;
    // Item 2ge: the Plan toggle, traced from the shape the operator supplied
    // at web/assets/source/brain-source.png -- a cartoon side-profile brain,
    // bold outline, lobes bunched up and to the left, a long sweep down the
    // right and a stem hanging off the lower right. The first attempt (v1.1.2)
    // drew a symmetrical diagram and missed; the asymmetry is most of what
    // makes it read as a brain rather than a cloud. Line art only: one outline
    // path, the folds, the stem, stroke with no fill and no highlights, at the
    // header glyph size in --accent-plan. The source image stays local, as
    // CLAUDE.md requires since the history rewrite; only this derived path ships.
    link.innerHTML = '<svg class="shell-page-icon shell-page-brain" viewBox="0 0 24 24" aria-hidden="true">'
      + '<path d="M9.6 4.6 c-1.7-.6-3.6.2-4.3 1.9 c-1.6.1-2.9 1.5-2.8 3.1 c-1.3.8-1.7 2.5-1 3.8 c-.8 1.3-.4 3 .9 3.8 c.1 1.6 1.5 2.8 3.1 2.7 c.8 1.4 2.6 1.9 4 1.1 c1.2 1 3 .9 4.1-.3 c1.7.4 3.4-.6 3.9-2.3 c1.6-.5 2.5-2.2 2.1-3.8 c1.2-1.1 1.3-2.9.3-4.1 c.5-1.6-.3-3.3-1.9-3.9 c-.2-1.6-1.6-2.8-3.2-2.6 c-.9-1.3-2.7-1.7-4.1-.8 Z"/>'
      + '<path d="M16.9 19.6c.5 1.3.7 2.4.6 3.4m2-4.3c-.7 1.2-1.1 2.4-1.2 3.6"/>'
      + '<path d="M11.4 4.2c.5 1.4.4 2.8-.3 4.1"/>'
      + '<path d="M11.1 8.3c-1.3.8-2.9.9-4.3.3"/>'
      + '<path d="M11.1 8.3c1.6.6 3.4.4 4.8-.6"/>'
      + '<path d="M5.6 12.1c2 1.2 4.5 1.3 6.6.3"/>'
      + '<path d="M12.2 12.4c1.9 1.3 4.4 1.4 6.4.2"/>'
      + '<path d="M7.4 16.1c1.8.9 3.9.9 5.7 0"/>'
      + '<path d="M5.3 9.6c.7.3 1.2.9 1.4 1.6"/>'
      + '<path d="M4.6 15.1c.7-.1 1.4.1 1.9.6"/>'
      + '<path d="M19.4 10.1c-.7.4-1.2 1-1.4 1.8"/>'
      + '</svg>';
    link.setAttribute("aria-label", "plan");
    link.title = "plan";
    link.href = path;
    if (page === id) {
      // Item 2gf: on the Plan page the toggle returns to the chat. It used to
      // preventDefault, so the operator who reached Plan had no route back
      // from the control that brought him — the capture in W1 shows the page
      // he was left on.
      link.setAttribute("aria-current", "page");
      link.title = "chat";
      link.setAttribute("aria-label", "chat");
      link.onclick = (event) => {
        event.preventDefault();
        returnToChat();
      };
    }
    pages.append(link);
  }
  const settings = node("a", "shell-settings");
  settings.textContent = "⚙";
  settings.setAttribute("aria-label", "Settings");
  settings.title = "Settings";
  settings.addEventListener("click", () => {
    const closing = settings.getAttribute("aria-expanded") === "true";
    beginNavigation({ kind: "settings", from: closing ? "settings" : page, to: closing ? page : "settings", fullDocument: false, chatID: store.active, mutationToken: store.mutation_token });
  });
  right.append(sessionHeading, pages, settings);
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
    if (!menu) return;
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
      .filter((session) => session.role !== "c" && `agent_${session.role === "d" ? "d" : "b"}` === agentID && (includeClosed || !session.closed))
      .sort((a, b) => Date.parse(b.created_at || 0) - Date.parse(a.created_at || 0));
  }

  function agentName(agentID) {
    const selected = store.sessions[store.selection.session_id];
    if (agentID === "agent_b") {
      if (selected?.agent_name) return selected.agent_name;
    }
	const selectedAgentID = selected?.agent_id || agentKey(store.config.agents?.[0]);
	const configured = (store.config.agents || []).find((agent) => agentKey(agent) === selectedAgentID) || store.config.agents?.[0];
	const profileID = configured?.[agentID.replace("agent_", "")];
	const profile = (store.servers || []).find((item) => item.id === profileID);
    return profile?.label || profileID || agentID;
  }

  function agentState(agentID) {
    const sessions = sessionsFor(agentID, false);
    if (sessions.some((item) => item.model_unreachable)) return "offline";
    if (sessions.some((item) => item.pending_approval || item.pending_repo_policy || item.run?.status === "paused")) return "waiting";
    if (sessions.some((item) => activeRunStates.has(item.run?.status))) return "running";
    return "idle";
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
    const open = Object.values(store.sessions)
      .filter((session) => session.role !== "c" && (!session.closed || session.id === store.selection.session_id))
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
      const chatName = session?.label || agentName(agentID);
      const tab = button("", chatName, `agent-tab ${selected ? "selected" : ""}`);
      const glyphState = session ? chatState(session) : agentState(agentID);
      tab.dataset.agent = agentID;
      if (session) tab.dataset.session = session.id;
      tab.setAttribute("aria-label", `${agentID} chat · ${chatName}`);
      const robot = agentID.slice(-1);
      tab.innerHTML = `<span class="agent-tab-robot agent-tab-robot-${robot} ${glyphState}" aria-hidden="true"><img src="/static/assets/agent.svg" alt=""><span class="agent-tab-eyes"></span></span><span>${escapeHTML(agentID)}</span>`;
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
          document.dispatchEvent(new CustomEvent("settings.close", { detail: { surface: "chat" } }));
          openSide(agentID, session.id, "chat");
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
        const close = button("×", `Close ${chatName}`, "agent-tab-close");
        close.disabled = store.replay || isRunning(session);
        close.onclick = (event) => { event.stopPropagation(); void closeChat(session, menu, agentID); };
        wrap.append(close);
      }
      wrap.append(menu);
      tabs.append(wrap);
    }
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
    const openCount = sessions.filter((session) => !session.closed).length;
    const count = node("div", "agent-chat-count");
    count.textContent = `${sessions.length} ${sessions.length === 1 ? "chat" : "chats"} · ${openCount} open · ${sessions.length - openCount} closed`;
    menu.append(count);
    if (!sessions.length) {
      const empty = node("span", "shell-menu-empty");
      empty.textContent = "No chats";
      menu.append(empty);
      return;
    }
    for (const session of sessions) {
      const row = node("div", `agent-chat-row ${session.closed ? "closed" : "open"}`);
      row.dataset.session = session.id;
      const summary = node("span", "agent-chat-summary");
      summary.textContent = `${chatRowText(session)}${session.closed ? " · closed" : ""}`;
      summary.title = firstUserLine(session);
      const open = button("Open", `Open ${firstUserLine(session)}`, "agent-chat-open");
      open.textContent = session.closed ? "Reopen" : "Open";
      open.onclick = async () => {
        if (session.closed) {
          try {
            await api(`/api/sessions/${encodeURIComponent(session.id)}/reopen`, {});
            reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          } catch (error) { return report(error.message); }
        }
        setSelection(agentID, session.id); menu.hidden = true;
      };
      const rename = button("Rename", `Rename ${firstUserLine(session)}`, "agent-chat-rename");
      rename.onclick = () => showRename(row, session, menu, agentID);
      const close = button("×", `Close ${firstUserLine(session)}`, "agent-chat-close");
      close.disabled = session.closed || store.replay;
      close.onclick = () => void closeChat(session, menu, agentID);
      const remove = button("🗑", `Delete ${firstUserLine(session)} permanently`, "agent-chat-delete");
      remove.disabled = !session.closed || store.replay;
      if (!session.closed) remove.title = "Close this chat before deleting it permanently";
      remove.onclick = () => void armDelete(row, session, menu, agentID, summary, remove);
      row.append(summary, open, rename, close, remove);
      menu.append(row);
    }
  }

  async function armDelete(row, session, menu, agentID, summary, remove) {
    try {
      const preview = await api(`/api/sessions/${encodeURIComponent(session.id)}/delete`, { confirm: false });
      const inventory = preview.inventory || {};
      const writes = inventory.memory_writes || [];
      summary.textContent = `Delete permanently? ${inventory.events || 0} events · ${inventory.jsonl_files || 0} files · ${writes.length} memory kept`;
      const dropLabel = node("label", "agent-chat-drop-memory");
      const dropMemory = document.createElement("input");
      dropMemory.type = "checkbox";
      dropLabel.append(dropMemory, " drop memory");
      remove.textContent = "delete";
      remove.classList.add("armed");
      remove.setAttribute("aria-label", `Confirm permanent delete of ${firstUserLine(session)}`);
      remove.onclick = async () => {
        try {
          await api(`/api/sessions/${encodeURIComponent(session.id)}/delete`, { confirm: true, drop_memory: dropMemory.checked });
          reduce({ type: "snapshot", data: await api("/api/state", undefined, "GET") });
          menu.hidden = true;
        } catch (error) { report(error.message); }
      };
      row.classList.add("delete-confirm");
      row.replaceChildren(summary, ...(writes.length ? [dropLabel] : []), remove);
    } catch (error) { report(error.message); }
  }

  function showRename(row, session, menu, agentID) {
    const editor = node("form", "agent-chat-rename-form");
    const input = document.createElement("input");
    input.value = session.label || firstUserLine(session);
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

  async function closeChat(session, menu, agentID) {
    if (isRunning(session)) return report("This chat has a running run. Stop it before closing the chat.");
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
    document.title = session ? sessionTitle(session) : "Agent_b";
    sessionHeading.hidden = !session;
    sessionHeading.textContent = session ? sessionTitle(session) : "";
    renderTabs();
    const query = new URLSearchParams();
    if (session) query.set("session", session.id);
    const suffix = query.size ? `?${query}` : "";
    for (const link of pages.children) link.href = `/${link.dataset.page}${suffix}`;
    const configured = configuredAgent(session);
    const planLink = pages.querySelector('[data-page="plan"]');
    if (planLink) planLink.hidden = !session || (session.role !== "d" && !!String(configured?.d || "").trim());
    // Item 2hb: on a page that is its own document the gear is a LINK, and the
    // document it opens has no other way to know where it came from. The view
    // being left is named in the address, so closing can return to it.
    settings.href = `/${suffix}${suffix ? "&" : "?"}from=${page}#settings/servers`;
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
