import { api, reduce, setSelection, store, subscribe } from "./bus.js";
import { chatName, chatRowText, isRunning, sessionTitle } from "./chat-lifecycle.js";
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
    // Item 2he: the processor, as drawn. The operator asked for "something more
    // symmetrical that represents planning/thought and is robotic", picked the
    // chip from three candidates, and it is checked in at
    // web/assets/plan-chip.svg. This is that file VERBATIM -- same viewBox,
    // same rects, same pin path, same stroke-width 2 -- not a redraw and not a
    // simplification. The traced brain it replaces is gone, with its source
    // reference and plan-brain.svg.
    //
    // The operator halved the displayed chip in v1.5.0; its checked-in geometry
    // remains unchanged and scales inside the smaller header box.
    link.innerHTML = '<svg class="shell-page-chip" viewBox="0 0 24 24" width="12" height="12"'
      + ' fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"'
      + ' stroke-linejoin="round" aria-hidden="true">'
      + '<rect x="7" y="7" width="10" height="10" rx="2"/>'
      + '<rect x="10" y="10" width="4" height="4" rx="0.5"/>'
      + '<path d="M9 2v5M12 2v5M15 2v5M9 17v5M12 17v5M15 17v5M2 9h5M2 12h5M2 15h5M17 9h5M17 12h5M17 15h5"/>'
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
        const close = button("×", `Close ${name}`, "agent-tab-close");
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
    // Item 2go, the operator: "i want the chat summary removed from the top of
    // chats... i want it to display like this: MM:DD · Chat name · × , nothing
    // more." So there is no summary line, and the row below carries nothing
    // else either.
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
      const close = button("×", `Close ${chatName(session)}`, "agent-chat-close");
      close.disabled = session.closed || store.replay;
      close.onclick = () => void closeChat(session, menu, agentID);
      row.append(summary, rename, close);
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

  // Item 2gq (v1.2.5): close deletes, so it asks first. One line, the same
  // dialog the removed permanent-delete control used, and it says plainly what
  // is NOT lost - because what the chat produced elsewhere is not the chat.
  const closeConfirmText = "Delete this chat? Its memory notes, plans and files stay.";

  async function closeChat(session, menu, agentID) {
    if (isRunning(session)) return report("This chat has a running run. Stop it before closing the chat.");
    if (!window.confirm(closeConfirmText)) return;
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
    // do is say which chat is in the window instead of naming the profile,
    // which the header beside the tab strip already says.
    document.title = session ? `Agent_b · ${chatName(session)}` : "Agent_b";
    sessionHeading.hidden = !session;
    const heading = session ? sessionTitle(session) : "";
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
