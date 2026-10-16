import { installComposerResize } from "./composer-resize.js";
import { api, loadSessionHistory, reduce, setSelection, store, subscribe, subscribeBeforePatch } from "./bus.js";
import { renderMarkdown } from "./markdown.js";
import { setWaitProgress, waitElement } from "./wait.js";
import { operatorLogEntry } from "./operator-log.js";
import { createThinkingRenderer } from "./reasoning.js";
import { formatDuration } from "./duration.js";
import { createAttachmentBubble, createFileChip, fileURL, filesFromResponse, probeFile } from "./deliverables.js";
import { createApprovalCard } from "./approval.js";
import { callServiceKey, callServiceStatus } from "./call-service-display.js";
import { attachmentChipFile, attachmentMetadata, exchangeFiles, exchangeUpload, uploadAttachment } from "./attachment-upload.js";
import { attachmentReadability } from "./attachment-readability.js";
import { agentAuthor, isRunning, openSessions, sameWorkerPlan, workerApproval } from "./chat-lifecycle.js";
import { renderSendStop } from "./stop-state.js";
import { runTimeSentence } from "./run-summary.js";
import { groupResponseRows, hasVisibleChatContent, isHeaderlessSteps, itemFailed, responseBlocks, responseHasOnlyThoughts, responseStepOutcome, responseSummary, responseSummarySentence, shortToolTarget } from "./chat-response-groups.js";
import { navigationSurfaceReady } from "./navigation-telemetry.js";
import { registerMenu } from "./menu-behavior.js";
import { liveActivityLook, liveActivityText, showsStreamCaret } from "./chat-activity.js";
import { createAgentRobot, updateAgentRobot } from "./agent-robot.js";
import { renderChatProposals } from "./chat-proposals.js";
import { installTranscriptCopy, responseTranscriptRecord, setTranscriptCopyRecord } from "./transcript-copy.js";

const budget = document.getElementById("chat-budget");
const log = document.getElementById("chat-log");
const input = document.getElementById("chat-task");
const statusLine = document.getElementById("chat-status-line");
const resizeHandle = document.getElementById("chat-resize-handle");
const send = document.getElementById("chat-send");
const notice = document.getElementById("chat-notice");
const pendingApproval = document.getElementById("chat-pending-approval");
const chatProposals = document.getElementById("chat-proposals");
const composer = document.querySelector(".chat-composer");
const attachButton = document.getElementById("chat-attach");
const attachMenu = document.getElementById("chat-attach-menu");
const attachBrowse = document.getElementById("chat-attach-browse");
const attachExchange = document.getElementById("chat-attach-exchange");
const exchangeFileList = document.getElementById("chat-exchange-files");
const attachMenuControl = registerMenu(attachMenu, { anchor: attachButton });
const filePicker = document.getElementById("chat-file-picker");
const pendingFiles = document.getElementById("chat-attachments");
// send and stop are one control, so there is one element for both.
const mic = document.getElementById("chat-mic");
const retryModel = document.getElementById("chat-retry-model");
let attachmentFolder = { name: "attachments", path: "" };
let requested = new URLSearchParams(location.search).get("session");
// the chat the operator asked for by name, so the stale-selection
// sweep never takes him off a closed chat he opened deliberately.
let askedFor = "";
const selectedID = () => store.selection.session_id;
// copying the transcript marks the kind of each entry. Installed
// once, on the document, and inert unless the selection is in the transcript.
installTranscriptCopy(log);
const expanded = new Set();
const touched = new Set();
let follow = true;
let localNotice = "";
let localAlarm = false;
let frame = 0;
let renderTimer = 0;
let clockTimer = 0;
let attachmentsBusy = false;
let activeUpload = null;
const cancelledUploads = new Set();
let dragDepth = 0;
let queuedAttachments = [];
const attachmentQueues = new Map();
let lastRender = 0;
let mounted = false;
let shell = null;
const renderIntervalMS = 50;
const thinkingRenderer = createThinkingRenderer({
  document,
  expanded,
  rerender: () => render(),
  format,
  formatDuration,
  uncounted: () => {
    const session = store.sessions[selectedID()];
    const connection = store.connections.find((value) => value.id === (session?.connection_id || session?.b_connection));
    return connection?.capabilities?.reasoning_emission === "inline";
  },
});
const entryViews = new Map();
const fileStates = new Map();
const fileViews = new Map();
const toolViews = new Map();
const composerFileViews = new Map();
const composerApprovalViews = new Map();
let composerWait = null;
let composerRobot = null;
let composerText = null;
const queuedDrafts = new Map();
const returningQueues = new Set();
let mirrorTakeButton = null;
let usedEntryViews = new Set();
let usedFileViews = new Set();
let usedToolViews = new Set();
subscribeBeforePatch((state, event) => cacheQueuedDraft(state.sessions[event.data?.session_id]));
const earlierButton = document.createElement("button");
earlierButton.type = "button";
earlierButton.className = "chat-earlier";
earlierButton.onclick = () => {
  follow = false;
	const session = store.sessions[selectedID()];
	if (session) void loadSessionHistory(session.id, true);
};
const jumpButton = document.createElement("button");
jumpButton.type = "button";
jumpButton.className = "chat-jump";
jumpButton.textContent = "↓";
jumpButton.ariaLabel = "Jump to latest";
jumpButton.title = "Jump to latest";
jumpButton.onclick = () => {
  follow = true;
	const session = store.sessions[selectedID()];
	if (session && Number(session.history_end || session.history_total || 0) < Number(session.history_total || 0)) void loadSessionHistory(session.id, false);
	else renderLog(session);
};
// Reading settings are custom properties on the document root. Chat CSS keeps
// every relative size tied to --chat-size, and prose alone inherits --chat-ink.
const NAMED_PIXELS = { small: 11, normal: 12, large: 14, larger: 16, largest: 18 };
const TEXT_COLORS = { default: "#E8EEF4", white: "#FFFFFF", red: "#FF6B6B", orange: "#FF9F43", yellow: "#FFD166", green: "#3CF08A", cyan: "#4DDFF7", blue: "#6CA8FF", purple: "#C792EA", pink: "#FF85C2" };

// Twelve pixels is the previous "normal" step and remains the unset default.
const DEFAULT_TEXT_SIZE = 12;

function applyReadingSettings() {
  const chat = store.config?.chat || {};
  const pixels = Number.isInteger(chat.text_size) ? chat.text_size : (NAMED_PIXELS[chat.text_size] || DEFAULT_TEXT_SIZE);
  const face = (chat.typeface || "").trim();
  const root = document.documentElement;
  const boundedPixels = Math.min(32, Math.max(9, pixels));
  root.style.setProperty("--chat-size", `${boundedPixels}px`);
  root.dataset.chatListWrap = String(boundedPixels >= 28);
  root.style.setProperty("--chat-ink", TEXT_COLORS[chat.text_color] || TEXT_COLORS.default);
  // An unset typeface is not a broken one: it falls back to the product's own
  // sans, which is what every release before rendered.
  if (face) root.style.setProperty("--chat-face", `"${face}", var(--sans)`);
  else root.style.removeProperty("--chat-face");
}

subscribe((_state, event) => {
	if (event.type === "attachment.ocr_progress" && activeUpload?.id === event.data?.upload_id) {
		activeUpload.page = event.data.page; activeUpload.total = event.data.total;
	}
  if (event.type === "snapshot") {
    applyReadingSettings();
    const open = newestOpenSessions();
    // a link that names a chat opens THAT chat, closed or open. The
    // `!closed` test sent the user to whatever chat happened to be
    // selected — five of five ids in the probe — and nine of his ten
    // retained chats are closed, so it was his whole history.
    if (requested && store.sessions[requested]) { askedFor = requested; changeBound(requested); requested = ""; }
    else if (store.selection.agent_id === "agent_b" && (!store.sessions[selectedID()] || store.sessions[selectedID()].closed)) changeBound(open[0]?.id || "");
  }
  if (event.type === "config.changed") applyReadingSettings();
	cacheQueuedDraft(store.sessions[selectedID()]);
  // The sweep below exists for a selection that went stale on its own. A chat
  // the operator asked for by name is not stale, so it is left in front of him
  // until he chooses another.
  // `askedFor` is not cleared when the selection moves: the selection is
  // applied through the bus and does not always equal the id on the very
  // snapshot that set it, and clearing it there is what made the first attempt
  // at this bounce straight back off the closed chat.
  if (!askedFor && store.selection.agent_id === "agent_b" && store.sessions[selectedID()]?.closed) changeBound(newestOpenSessions()[0]?.id || "");
  if (event.session_id && selectedID() && event.session_id !== selectedID()) {
    // The worker has no thread of its own: a change to its pending card is drawn
    // in the design thread of its plan, and at once, because it waits on the operator.
    if (sameWorkerPlan(store.sessions, event.session_id, selectedID())) renderNow();
    return;
  }
  // A question waiting on the operator is not drawn on an animation frame: a
  // page that is not in the foreground gets none, and the worker would be
  // waiting on an answer nobody was shown.
  if (waitsOnTheOperator(event)) { renderNow(); return; }
  schedule();
});

function waitsOnTheOperator(event) {
  if (event?.type !== "projection.patch") return false;
  return (event.data?.operations || []).some((operation) =>
    String(operation?.path || "").startsWith("/chat") && operation?.value?.event?.type === "c.job");
}

function renderNow() {
  if (!mounted) return;
  if (frame) { cancelAnimationFrame(frame); frame = 0; }
  if (renderTimer) { clearTimeout(renderTimer); renderTimer = 0; }
  lastRender = performance.now();
  render();
}

function schedule() {
  if (!mounted) return;
  if (frame || renderTimer) return;
  const delay = Math.max(0, renderIntervalMS - (performance.now() - lastRender));
  renderTimer = setTimeout(() => {
    renderTimer = 0;
    frame = requestAnimationFrame(() => {
      frame = 0;
      lastRender = performance.now();
      render();
    });
  }, delay);
}

function render() {
  if (!mounted) return;
  const session = store.sessions[selectedID()];
	reconcileQueuedDraft(session);
  renderBudget(session);
  renderLog(session);
  renderComposer(session);
  navigationSurfaceReady("chat", store);
}

function reconcileQueuedDraft(session) {
	if (!session) return;
	cacheQueuedDraft(session);
	const ids = Array.isArray(session.queued_message_ids) ? session.queued_message_ids : [];
	if (ids.length) {
		if (session.run?.status === "idle" && session.run?.last_stop_reason !== "model_unreachable" && !session.model_unreachable && !returningQueues.has(session.id)) {
			returningQueues.add(session.id);
			void api("/api/stop", { session_id: session.id }).finally(() => returningQueues.delete(session.id));
		}
		return;
	}
	const messages = queuedDrafts.get(session.id);
	if (!messages?.length) return;
	if (session.run?.status === "stopping" || session.run?.status === "idle") {
		const returned = messages.map((message) => message.text).filter(Boolean).join("\n");
		if (selectedID() === session.id && returned) input.value = [returned, input.value].filter(Boolean).join("\n");
		const attachments = messages.flatMap((message) => message.attachments || []);
		if (selectedID() === session.id && attachments.length) {
			queuedAttachments = [...attachments, ...queuedAttachments.filter((candidate) => !attachments.some((item) => item.path.toLowerCase() === candidate.path.toLowerCase()))];
			attachmentQueues.set(session.id, queuedAttachments);
		}
		queuedDrafts.delete(session.id);
	} else if (session.run?.status === "running") queuedDrafts.delete(session.id);
}

function cacheQueuedDraft(session) {
	if (!session) return;
	const ids = Array.isArray(session.queued_message_ids) ? session.queued_message_ids : [];
	if (!ids.length) return;
	const byID = new Map((session.chat || []).filter((entry) => entry.type === "user").map((entry) => [String(entry.key || "").replace(/^message:/, ""), entry]));
	const messages = ids.map((id) => byID.get(id)).filter(Boolean).map((entry) => ({ text: entry.text || "", attachments: entry.attachments || [] }));
	if (messages.length) queuedDrafts.set(session.id, messages);
}

export function mountChat(shellController) {
  shell = shellController;
  mounted = true;
  void loadMicAvailability();
  schedule();
  clockTimer = setInterval(() => { if (isRunning(store.sessions[selectedID()])) schedule(); }, 1000);
}

export function unmountChat() {
  mounted = false;
  if (frame) cancelAnimationFrame(frame);
  if (renderTimer) clearTimeout(renderTimer);
  frame = 0;
  renderTimer = 0;
  if (clockTimer) clearInterval(clockTimer);
  clockTimer = 0;
  dragDepth = 0;
  document.body.classList.remove("drop-target");
}

// THE PANE MAY ONLY FALL BACK TO A CHAT THE STRIP WOULD SHOW.
//
// This is the user's sighting, found against a copy of his own restored journal
// set: of his 34 chats, 33 are closed and the one that is not is s23, role `c` — a
// WORKER. The strip refuses role c ("a worker has no chat"), and this fallback only
// filtered `closed`, so with every chat of his own closed the window bound to the
// worker's transcript and no tab was lit. "when no chat tabs are open its showing me
// an old chat still in the window."
//
// One rule, in one place: open, and something the strip would draw. In replay there
// is no such distinction to preserve — there is only what was replayed — which is
// the established rule and is kept.
function newestOpenSessions() {
  return openSessions(store.sessions)
    .filter((session) => store.replay || session.role !== "c")
    .sort((left, right) => Date.parse(right.created_at || 0) - Date.parse(left.created_at || 0));
}

function renderBudget(session) {
  const value = session?.budget || {};
  const used = value.used_measured || value.used_est || 0;
  const ceiling = value.ceiling || 0;
  const ratio = ceiling ? used / ceiling : 0;
  setAttribute(budget, "class", `chat-budget ${ratio > 1 ? "over" : ratio > 0.85 ? "warn" : ""}`);
  const width = `${Math.min(100, ratio * 100)}%`;
  const fill = budget.querySelector(".chat-budget-fill");
  if (fill.style.width !== width) fill.style.width = width;
  setText(budget.querySelector(".chat-budget-tip"), `${value.estimated ? "estimated · " : ""}${format(used)} / ${format(ceiling)}`);
}

function renderLog(session) {
  const wasBottom = follow;
  thinkingRenderer.begin();
  usedEntryViews = new Set();
  usedFileViews = new Set();
  usedToolViews = new Set();
  // before the first snapshot nothing is known, so nothing is claimed.
  if (!store.loaded) {
    finishLogRender([]);
    return;
  }
  // The opening event-stream snapshot carries metadata for every retained
  // chat but no transcript rows. A deep link starts the bounded history fetch
  // as soon as that metadata arrives; until it finishes, an empty chat would
  // be a false claim and can flash during reloads or on a slow connection.
  if (session && Number(session.history_total || 0) > 0 && !(session.chat || []).length) {
    finishLogRender([]);
    return;
  }
  if (!session) {
    const empty = document.createElement("div");
    empty.className = "chat-empty";
    const noAgent = !(store.config.agents || []).length;
    const face = createAgentRobot(document, { place: "well" });
    const line = document.createElement("span");
    if (noAgent) {
      line.append("No agent connected — add one in ");
      const settings = document.createElement("a"); settings.textContent = "Settings"; settings.href = "/#settings/connections";
      line.append(settings);
    } else {
      const launch = document.createElement("button"); launch.type = "button"; launch.textContent = "New chat"; launch.onclick = () => shell?.newChat();
      line.append(launch);
    }
    empty.append(face, line);
    finishLogRender([empty]);
    return;
  }
  let entries;
  try {
    entries = buildEntries(session);
  } catch (error) {
    finishLogRender([renderHistoryFailure(error)]);
    return;
  }
  if (!entries.length) {
    const empty = document.createElement("div");
    empty.className = "chat-empty";
    empty.append(session.runnable ? "Send a task to start the loop." : session.not_runnable_reason);
    const replacement = !session.runnable && session.not_runnable_reason === "connection not found"
      ? store.connections.find((connection) => connection.label === session.b_connection)
      : null;
    if (replacement) {
      const rebind = document.createElement("button");
      rebind.type = "button";
      rebind.textContent = `use ${replacement.label}`;
      rebind.onclick = async () => {
        try {
          const result = await api(`/api/sessions/${encodeURIComponent(session.id)}/rebind`, {});
          reduce({ type: "session.updated", data: result.session });
        } catch (error) {
          localNotice = error.message; localAlarm = true; renderComposer(session);
        }
      };
      empty.append(" ", rebind);
    }
    finishLogRender([empty]);
    return;
  }
  const nodes = [];
	const start = 0, end = entries.length;
	if (Number(session.history_start || 0) > 0) {
		earlierButton.textContent = `earlier: ${session.history_start} entries`;
    nodes.push(earlierButton);
  }
  for (const [index, entry] of entries.slice(start, end).entries()) nodes.push(renderEntrySafely(session, entry, start + index));
	const atLatest = Number(session.history_end || session.history_total || 0) >= Number(session.history_total || 0);
	setProperty(jumpButton, "hidden", follow && atLatest);
  nodes.push(jumpButton);
  finishLogRender(nodes);
  requestAnimationFrame(() => {
		if (wasBottom && atLatest) log.scrollTop = log.scrollHeight;
		setProperty(jumpButton, "hidden", follow && atLatest);
  });
}

function finishLogRender(nodes) {
  const session = store.sessions[selectedID()];
  if (statusLine && (isRunning(session) || !statusLine.hidden)) nodes.push(statusLine);
  reconcileChildren(log, nodes);
  thinkingRenderer.end();
  for (const key of entryViews.keys()) if (!usedEntryViews.has(key)) entryViews.delete(key);
  for (const key of fileViews.keys()) if (!usedFileViews.has(key)) fileViews.delete(key);
  for (const key of toolViews.keys()) if (!usedToolViews.has(key)) toolViews.delete(key);
}

function reconcileChildren(parent, nodes) {
  const wanted = new Set(nodes);
  for (const child of [...parent.children]) if (!wanted.has(child)) child.remove();
  let cursor = parent.firstElementChild;
  for (const node of nodes) {
    if (node === cursor) cursor = cursor.nextElementSibling;
    else parent.insertBefore(node, cursor);
  }
}

function buildEntries(session) {
  const chat = Array.isArray(session?.chat) ? session.chat : [];
  return groupResponses(chat.filter((entry) => !["operator.context", "message.queued", "run.queued"].includes(entry?.event?.type)).filter(hasVisibleChatContent));
}

function changeBound(value) {
  const previous = selectedID();
  if (previous) attachmentQueues.set(previous, queuedAttachments);
  setSelection(`agent_${store.sessions[value]?.role === "d" ? "d" : "b"}`, value);
  queuedAttachments = attachmentQueues.get(value) || [];
  // "Build plan now?" opens the planning chat with its request in
  // the composer; the operator sends it, so the harness speaks for no one.
  try {
    const draft = sessionStorage.getItem(`agentb.draft.${value}`);
    if (draft !== null) {
      sessionStorage.removeItem(`agentb.draft.${value}`);
      if (!input.value) input.value = draft;
    }
  } catch { /* no draft */ }
}

// The projection names this field agent_role; read it in one place so the
// author of a c-role notice cannot go missing the way it did.
function entryRole(entry) { return entry?.agent_role || entry?.agentRole || "b"; }

function groupResponses(entries) {
  const grouped = [];
  let response = null;
  let boundary = "orphan";
  for (const entry of entries) {
    if (entry?.type === "user" || entry?.type === "summary") {
      grouped.push(entry);
      boundary = entry.key || `${entry.type}:${grouped.length}`;
      response = null;
      continue;
    }
    if (!entry || typeof entry !== "object") {
      grouped.push(entry);
      response = null;
      continue;
    }
    // A server-authored notice may carry only its words. At a response boundary
    // it is a transcript notice in its own right; between response steps it
    // stays with those steps and uses the same renderer there.
    if (!response && entry.type === "notice" && (!entry.event || typeof entry.event !== "object") && typeof entry.text === "string" && entry.text.length) {
      grouped.push(entry);
      continue;
    }
    // A notice that is waiting on the operator is never folded into a steps
    // group: an unreachable model, and a question from the worker, are the two
    // things in this thread that nobody can answer without seeing them. Item
    // 2fg: nor is a run stopped mid-tool, so the transcript shows the stop.
    // nor is a finished run's time sentence, for a different
    // reason — it is the line that CLOSES the run, and folding it into the steps
    // group buries it under a collapsed "Steps · 1 row", which is where my first
    // version of this put it. The screenshot gate is what showed me.
    if (entry.type === "notice" && ((entry.event?.type === "run.stopped" && entry.event?.data?.reason === "model_unreachable") || (entry.event?.type === "run.stopped" && entry.event?.data?.reason === "aborted_mid_tool") || (entry.event?.type === "run.stopped" && entry.event?.data?.reason === "done") || (entry.event?.type === "compaction" && entry.event?.data?.kind === "fresh") || entry.event?.type === "service.identity_unavailable" || entry.event?.type === "c.job")) {
      grouped.push(entry);
      response = null;
      continue;
    }
		const role = entryRole(entry);
		if (response && entryRole(response) !== role) { boundary = entry.key || `${role}:${grouped.length}`; response = null; }
    if (!response) {
			response = { type: "response", key: `response:${boundary}`, agent_role: role, run_id: entry.run_id || "", items: [] };
      grouped.push(response);
    }
    if (!response.run_id && entry.run_id) response.run_id = entry.run_id;
    response.items.push(entry);
  }
  return grouped;
}

function renderEntrySafely(session, entry, index) {
  try {
    return renderEntry(session, entry);
  } catch (error) {
    return renderFailure(entry, error, index, false);
  }
}

// a key that appears twice in one render gets its own
// row the second time.
//
// The transcript was rendering the retained chats out of order — s6 showed
// m-5, m-9, then went back to m-1, m-3. The cause is not ordering code: the
// the projector list, the client copy and the entries handed to the renderer
// are all in journal order. It is that the journal contains the SAME message
// id more than once (s6 repeats m-1, m-3 and m-7), the view cache is keyed by
// that id, so the second occurrence was handed the DOM node of the first, and
// insertBefore MOVES a node it already holds instead of adding one. Eighteen
// entries collapsed to twelve rows and the survivors ended up in the order
// they were last moved to.
//
// Dropping a repeat would hide part of the record, and renaming keys would
// change what every gate and the audit compare. So a repeat is drawn as its
// own row: the transcript shows what the journal holds, in the journal
// order. The duplicate ids themselves are a separate defect.
function viewKeyFor(entry) {
  if (!usedEntryViews.has(entry.key)) return entry.key;
  let suffix = 2;
  while (usedEntryViews.has(`${entry.key}#${suffix}`)) suffix++;
  return `${entry.key}#${suffix}`;
}

function renderEntry(session, entry) {
  if (!entry || typeof entry !== "object") throw new Error("entry is missing or is not an object");
  if (!entry.key) throw new Error("entry key is missing");
  if (entry.type === "notice") return renderNotice(session, entry);
  if (entry.type === "response") return renderResponse(session, entry);
  const viewKey = viewKeyFor(entry);
  let view = entryViews.get(viewKey);
  if (!view) {
    const row = document.createElement("section");
    row.tabIndex = 0;
    const content = document.createElement("div");
    content.className = "chat-content";
		row.append(content);
		view = { row, content, text: "" };
    entryViews.set(viewKey, view);
  }
  usedEntryViews.add(viewKey);
  setAttribute(view.row, "data-entry-key", entry.key);
	setAttribute(view.row, "aria-label", entry.type === "user" ? "you" : entry.type === "summary" ? "summary" : agentAuthor(session, entryRole(entry)));
  setAttribute(view.row, "class", `chat-entry ${entry.type === "user" ? "chat-user" : entry.type === "summary" ? "chat-summary" : entry.type === "tool" ? "tool-entry" : "chat-agent"}`);
  const content = view.content;
  if (entry.type === "user") {
    setAttribute(content, "class", "chat-content chat-bubble");
    const nodes = [];
    if (entry.text) {
      if (!view.userText) { view.userText = document.createElement("div"); view.userText.className = "chat-user-text"; }
      if (view.text !== entry.text) view.userText.textContent = entry.text;
      nodes.push(view.userText);
    }
    for (const attachment of entry.attachments || []) {
      nodes.push(renderFileChip(session, attachmentChipFile(attachment)));
    }
		const messageID = String(entry.key || "").replace(/^message:/, "");
		const queued = (session.queued_message_ids || []).includes(messageID);
		if (queued) {
			if (!view.queued) { view.queued = document.createElement("span"); view.queued.className = "chat-queued-state"; }
			view.queued.textContent = session.model_unreachable ? "queued · waiting for model" : "queued";
			nodes.push(view.queued);
		}
    reconcileChildren(content, nodes);
    view.text = entry.text;
  }
  else if (entry.type === "summary") {
    if (view.text !== entry.text) content.textContent = entry.text || "";
    view.text = entry.text || "";
  }
  else throw new Error(`unsupported top-level entry type ${String(entry.type || "(missing)")}`);
  setTranscriptCopyRecord(view.row, entry.type === "user"
    ? `you: ${entry.text || ""}`
    : `— summary —\n${entry.text || ""}`);
  return view.row;
}

function renderResponse(session, entry) {
  const viewKey = viewKeyFor(entry);
  let view = entryViews.get(viewKey);
  if (!view) {
    const row = document.createElement("section");
    row.className = "chat-entry chat-agent chat-response";
    row.tabIndex = 0;
    const content = document.createElement("div");
    content.className = "chat-content chat-response-content";
    const rows = document.createElement("div");
    rows.className = "chat-response-rows";
    content.append(rows);
		row.append(content);
		view = { row, content, rows, blocks: new Map(), work: null };
    entryViews.set(viewKey, view);
  }
  usedEntryViews.add(viewKey);
	const author = agentAuthor(session, entryRole(entry));
	setAttribute(view.row, "aria-label", author);
	if (author !== "agent_b") {
		if (!view.label) { view.label = document.createElement("span"); view.label.className = "chat-notice-author chat-response-author"; }
		setText(view.label, author); reconcileChildren(view.content, [view.label, view.rows]);
	} else reconcileChildren(view.content, [view.rows]);
  const totals = responseSummary(entry.items);
  const active = isRunning(session) && entry.items.some((item) => item?.run_id && item.run_id === session.run?.run_id);
  const blocks = responseBlocks(entry.items);
  const stream = session?.activity?.stream;
  if (active && !stream?.done && Array.isArray(stream?.tool_calls) && stream.tool_calls.length) {
    blocks.push({
      key: `response-block:stream:${stream.key}`,
      prose: null,
      steps: stream.tool_calls.map((call) => ({ ...call, type: "streaming-tool", key: `streaming-tool:${stream.key}:${call.index}` })),
    });
  }
  const directThoughts = responseHasOnlyThoughts(entry.items);
  toggleClass(view.row, "thought-only-response", directThoughts);
  const lastWorkIndex = blocks.reduce((last, block, index) => block.prose || block.steps.length ? index : last, -1);
  const alarm = blocks.some((block, index) => responseStepOutcome(block.steps, { movedPast: index < lastWorkIndex }).alarm);
  toggleClass(view.row, "alarm", alarm);
  const usedBlocks = new Set(blocks.map((block) => block.key));
  const finalIndex = !active ? blocks.findLastIndex((block) => block.prose) : -1;
  const foldFinished = finalIndex >= 0 && blocks.some((block, index) => index !== finalIndex && (block.prose || block.steps.length));
  if (foldFinished) {
    const final = blocks[finalIndex];
    let work = blocks.filter((_, index) => index !== finalIndex);
    if (final.steps.length) work.push({ ...final, key: `${final.key}:steps`, prose: null });
    const todos = work.flatMap((block) => block.steps.filter((item) => item?.type === "tool" && item.name === "todo"));
    work = work.map((block) => ({ ...block, steps: block.steps.filter((item) => item?.name !== "todo") })).filter((block) => block.prose || block.steps.length);
    const nodes = work.length ? [renderFinishedWork(session, view, entry.key, entry.run_id, work)] : [];
    if (todos.length) {
      const todo = { key: `${entry.key}:todo`, prose: null, steps: [todos.at(-1)] };
      usedBlocks.add(todo.key);
      nodes.push(renderResponseBlock(session, view, todo, false, { forceOpen: true }));
    }
    nodes.push(renderResponseBlock(session, view, { ...final, steps: [] }, false, { final: true }));
    reconcileChildren(view.rows, nodes);
  } else {
    reconcileChildren(view.rows, blocks.map((block, index) => renderResponseBlock(session, view, block, active, {
      current: active && index === lastWorkIndex,
      movedPast: index < lastWorkIndex,
    })));
  }
  for (const key of view.blocks.keys()) if (!usedBlocks.has(key)) view.blocks.delete(key);
  setTranscriptCopyRecord(view.row, responseTranscriptRecord(entry, expanded, agentAuthor(session), active));
  return view.row;
}

function renderFinishedWork(session, view, responseKey, runID, blocks) {
  const key = `${responseKey}:worked`;
  if (!view.work) {
    const root = document.createElement("div");
    root.className = "chat-work-fold";
    const head = document.createElement("div");
    head.type = "button";
    head.className = "chat-work-summary";
    const summaryToggle = document.createElement("button"); summaryToggle.type = "button"; summaryToggle.className = "chat-work-summary-toggle";
    const caret = document.createTextNode("");
    const fileLabel = document.createTextNode("");
    const rows = document.createElement("div");
    rows.className = "chat-work-rows";
    const controls = document.createElement("div"); controls.className = "chat-work-controls"; controls.append(head);
    root.append(controls, rows);
    summaryToggle.onclick = () => { touched.add(key); expanded.has(key) ? expanded.delete(key) : expanded.add(key); render(); };
    view.work = { root, head, summaryToggle, caret, fileLabel, rows, controls };
  }
  view.work.runID = runID || "";
  const open = expanded.has(key);
  const items = blocks.flatMap((block) => block.steps);
  const duration = responseSummary(items).duration;
  const steps = blocks.filter((block) => block.prose).length || blocks.length;
  setAttribute(view.work.summaryToggle, "aria-expanded", String(open));
  const changed = new Set(items.flatMap((item) => item?.type === "tool" && ["write_file", "edit_file"].includes(item.name) && item.result?.ok === true ? [item.args?.path] : item?.name === "delegate" ? (item.result?.delegate?.applied || []) : []).filter(Boolean));
  const remaining = Number.isInteger(view.work.remaining) ? view.work.remaining : changed.size;
  const prefix = `Worked ${formatDuration(duration) || "0 s"} · ${steps} ${steps === 1 ? "step" : "steps"}`;
  if (changed.size) {
    if (view.work.runID && !view.work.fileToggle) {
      const toggle = document.createElement("span"); toggle.className = "chat-work-files-toggle"; toggle.tabIndex = 0; toggle.setAttribute("role", "button");
      const activate = async () => {
        if (view.work.filesOpen) { view.work.filesOpen = false; view.work.files = null; view.work.diffs = {}; render(); return; }
        try {
          const query = `?session_id=${encodeURIComponent(session.id)}&run_id=${encodeURIComponent(view.work.runID)}`;
          const result = await api(`/api/file-undo${query}`, undefined, "GET");
          view.work.files = result.files || []; view.work.remaining = view.work.files.length; view.work.diffs = {}; view.work.filesOpen = true;
        } catch (error) { if (error.status === 410) view.work.available = false; else view.work.result = error.message; }
        render();
      };
      toggle.onclick = (event) => { event.stopPropagation(); void activate(); };
      toggle.onkeydown = (event) => { if (event.key === "Enter" || event.key === " ") { event.preventDefault(); event.stopPropagation(); void activate(); } };
      view.work.fileToggle = toggle; view.work.available = true;
    }
    const fileText = ` · ${remaining} file${remaining === 1 ? "" : "s"} · file tools only`;
    if (view.work.fileToggle) {
      view.work.fileToggle.textContent = fileText;
      setAttribute(view.work.fileToggle, "aria-label", `${remaining} files changed`);
      setAttribute(view.work.fileToggle, "aria-expanded", String(!!view.work.filesOpen));
    }
    view.work.fileLabel.nodeValue = fileText;
    const countNode = view.work.runID && view.work.available !== false ? view.work.fileToggle : view.work.fileLabel;
    setText(view.work.summaryToggle, prefix);
    view.work.caret.nodeValue = ` ${open ? "▾" : "▸"}`;
    const nodes = [view.work.summaryToggle, countNode, view.work.caret];
    if (nodes.length !== view.work.head.childNodes.length || nodes.some((node, index) => view.work.head.childNodes[index] !== node)) view.work.head.replaceChildren(...nodes);
  } else {
    setText(view.work.summaryToggle, prefix); view.work.caret.nodeValue = ` ${open ? "▾" : "▸"}`;
    const nodes = [view.work.summaryToggle, view.work.caret];
    if (nodes.length !== view.work.head.childNodes.length || nodes.some((node, index) => view.work.head.childNodes[index] !== node)) view.work.head.replaceChildren(...nodes);
  }
  if (changed.size && view.work.runID && !view.work.undo) {
    const undo = document.createElement("button"); undo.type = "button"; undo.className = "chat-work-undo"; undo.textContent = "Undo file changes";
    undo.onclick = async () => { if (!confirm("Undo file changes from this run? File tools only.")) return; undo.disabled = true; try { const result = await api("/api/file-undo", { session_id:session.id, run_id:view.work.runID, confirm:true }); view.work.result = [...(result.restored || []).map((name) => `restored ${name}`), ...(result.skipped || []).map((name) => `left changed ${name}`)].join(" · ") || "nothing to restore"; } catch (error) { view.work.result = error.message; undo.disabled = false; } render(); };
    view.work.undo = undo; view.work.controls.append(undo);
  }
  if (view.work.undo) view.work.undo.hidden = !changed.size || !view.work.runID;
  if (view.work.result) { if (!view.work.outcome) { view.work.outcome = document.createElement("span"); view.work.outcome.className = "chat-work-undo-result"; view.work.controls.append(view.work.outcome); } setText(view.work.outcome, view.work.result); }
  reconcileChildren(view.work.rows, open ? blocks.map((block) => renderResponseBlock(session, view, block, false, { forceOpen: true, movedPast: true })) : []);
  view.work.root.querySelector(":scope > .chat-file-list")?.remove();
  if (view.work.filesOpen && view.work.runID) view.work.root.append(renderFileUndoList(session, view.work));
  return view.work.root;
}

function renderFileUndoList(session, work) {
  const list = document.createElement("div"); list.className = "chat-file-list";
  for (const file of work.files || []) {
    const item = document.createElement("div"); item.className = "chat-file-item";
    const row = document.createElement("div"); row.className = "chat-file-row";
    const open = document.createElement("button"); open.type = "button"; open.className = "chat-file-open"; open.textContent = `${file.path} · ${file.kind}`; open.setAttribute("aria-expanded", String(!!work.diffs?.[file.id]));
    open.onclick = async () => {
      if (work.diffs?.[file.id]) { delete work.diffs[file.id]; render(); return; }
      const query = `?session_id=${encodeURIComponent(session.id)}&run_id=${encodeURIComponent(work.runID)}&file_id=${encodeURIComponent(file.id)}`;
      try { work.diffs ||= {}; work.diffs[file.id] = await api(`/api/file-undo${query}`, undefined, "GET"); } catch (error) { work.result = error.message; }
      render();
    };
    const undo = document.createElement("button"); undo.type = "button"; undo.className = "chat-file-undo"; undo.textContent = "Undo this file";
    undo.onclick = async () => {
      if (!confirm("Undo file changes from this run? File tools only.")) return;
      undo.disabled = true;
      try {
        const result = await api("/api/file-undo", { session_id:session.id, run_id:work.runID, file_id:file.id, confirm:true });
        work.result = [...(result.restored || []).map((name) => `restored ${name}`), ...(result.skipped || []).map((name) => `left changed ${name}`)].join(" · ") || "nothing to restore";
        if ((result.restored || []).length) { work.files = work.files.filter((entry) => entry.id !== file.id); work.remaining = work.files.length; delete work.diffs[file.id]; }
      } catch (error) { work.result = error.message; }
      render();
    };
    row.append(open, undo); item.append(row);
    const diff = work.diffs?.[file.id];
    if (diff) {
      if (diff.file?.changed_since) { const warning = document.createElement("div"); warning.className = "chat-file-changed"; warning.textContent = "Changed again since this run; Undo this file will leave it unchanged."; item.append(warning); }
      if (diff.message) { const message = document.createElement("div"); message.className = "chat-file-diff-message"; message.textContent = diff.message; item.append(message); }
      else {
        const body = document.createElement("div"); body.className = "chat-file-diff";
        for (const line of diff.lines || []) { const node = document.createElement("div"); node.className = `chat-file-diff-line ${line.kind}`; const old = document.createElement("span"), current = document.createElement("span"), text = document.createElement("code"); old.textContent = line.old_line || ""; current.textContent = line.new_line || ""; text.textContent = line.text; node.append(old, current, text); body.append(node); }
        if (diff.more) { const more = document.createElement("div"); more.className = "chat-file-diff-more"; more.textContent = `${diff.more.toLocaleString()} more`; body.append(more); }
        item.append(body);
      }
    }
    list.append(item);
  }
  return list;
}

function renderResponseBlock(session, view, block, active, state = {}) {
  let blockView = view.blocks.get(block.key);
  if (!blockView) {
    const root = document.createElement("div");
    root.className = "chat-response-block";
    blockView = { root, items: new Map(), prose: null, proseText: "", proseCaret: null, fold: null, head: null, rows: null, narration: null };
    root.addEventListener("pointerdown", () => { if (blockView.head?.getAttribute("aria-expanded") === "true") { touched.add(block.key); expanded.add(block.key); } }, true);
    view.blocks.set(block.key, blockView);
  }
  const directThoughts = responseHasOnlyThoughts(block.steps);
  const directAnswerThought = !!block.prose && directThoughts;
  const rootAlarm = block.steps.length && responseStepOutcome(block.steps, { movedPast: !!state.movedPast }).alarm;
  setAttribute(blockView.root, "class", `chat-response-block${state.final ? " chat-response-final" : ""}${block.prose && block.steps.length ? " has-narration" : ""}${directAnswerThought ? " direct-thought-answer" : ""}${rootAlarm ? " alarm" : ""}`);
  const nodes = [];
  // Reasoning is emitted before prose by the model, so its visible row precedes
  // the answer. A thought-only turn has no intermediate Steps disclosure at all.
  const fold = block.steps.length ? renderResponseStepFold(session, blockView, block, active, directThoughts, state) : null;
  const prose = block.prose ? renderResponseProse(blockView, block.prose) : null;
  if (prose && fold) {
    if (directAnswerThought) {
      nodes.push(fold, prose);
    } else {
      if (!blockView.narration) {
        blockView.narration = document.createElement("div");
        blockView.narration.className = "chat-narration-line";
      }
      reconcileChildren(blockView.narration, [prose, blockView.head]);
      nodes.push(blockView.narration, blockView.rows);
    }
  } else {
    if (fold) nodes.push(fold);
    if (prose) nodes.push(prose);
  }
  reconcileChildren(blockView.root, nodes);
  return blockView.root;
}

function renderResponseProse(view, item) {
  if (!view.prose) {
    view.prose = document.createElement("div");
    view.prose.className = "chat-response-prose";
    view.answer = document.createElement("div");
    view.answer.className = "chat-response-answer";
  }
  setAttribute(view.prose, "data-entry-key", item.key);
  if (view.proseText !== item.text) renderMarkdown(view.answer, item.text);
  view.proseText = item.text;
  const nodes = [view.answer];
  if (showsStreamCaret(item)) {
    if (!view.proseCaret) {
      view.proseCaret = document.createElement("span");
      view.proseCaret.className = "stream-caret";
    }
    nodes.push(view.proseCaret);
  }
  reconcileChildren(view.prose, nodes);
  return view.prose;
}

function renderResponseStepFold(session, view, block, active, directThoughts, state = {}) {
  if (!view.fold) {
    view.fold = document.createElement("div");
    view.fold.className = "chat-step-fold";
    view.head = document.createElement("button");
    view.head.type = "button";
    view.head.className = "chat-step-summary";
    view.head.onclick = () => {
      const top = view.head.getBoundingClientRect().top;
      const open = view.head.getAttribute("aria-expanded") === "true";
      touched.add(block.key);
      open ? expanded.delete(block.key) : expanded.add(block.key);
      render();
      requestAnimationFrame(() => { if (view.head.isConnected) log.scrollTop += view.head.getBoundingClientRect().top - top; });
    };
    view.rows = document.createElement("div");
    view.rows.className = "chat-step-rows";
    view.fold.append(view.head, view.rows);
  }
  const totals = responseSummary(block.steps);
  // one tool call and one thought are two rows, not a group.
  // Active rows are pinned open, so drawing a disclosure for them would be an
  // inert control. Thought-only rows never need a grouping disclosure either.
  const inline = !!block.prose;
  const headerless = (inline && directThoughts) || (!inline && (state.current || directThoughts || isHeaderlessSteps(block.steps)));
  if (!touched.has(block.key) && view.head.getAttribute("aria-expanded") === "true" && (view.root.matches(":hover") || selectionWithin(view.root))) {
    touched.add(block.key); expanded.add(block.key);
  }
  const open = touched.has(block.key) ? expanded.has(block.key) : state.forceOpen || state.current || headerless || (!active && expanded.has(block.key));
  setProperty(view.head, "hidden", headerless);
  toggleClass(view.fold, "headerless", headerless);
  const outcome = responseStepOutcome(block.steps, { movedPast: !!state.movedPast });
  toggleClass(view.fold, "alarm", outcome.alarm);
  setAttribute(view.head, "aria-expanded", String(open));
  setResponseSummaryWithDuration(view.head, totals, open, block.steps.length);
  const usedItems = new Set(block.steps.map((item, index) => item?.key || `invalid:${index}`));
  const nodes = [];
  if (open) {
    const rows = state.forceOpen || active ? block.steps : groupResponseRows(block.steps);
    for (const [index, item] of rows.entries()) {
      try {
        if (item?.kind === "tool-group") {
          usedItems.add(item.key);
          nodes.push(renderResponseToolGroup(session, view, item, outcome.retried > 0));
        } else {
          const key = item?.key || `invalid:${index}`;
          nodes.push(renderResponseItem(session, view, item, key, false, outcome.retried > 0));
        }
      } catch (error) {
        nodes.push(renderFailure(item, error, index, true));
      }
    }
    const files = filesFromResponse(block.steps.filter((item) => item && typeof item === "object"));
    if (files.length) {
      if (!view.chips) {
        view.chips = document.createElement("div");
        view.chips.className = "file-chips";
      }
      reconcileChildren(view.chips, files.map((file) => renderFileChip(session, file)));
      nodes.push(view.chips);
    }
  }
  reconcileChildren(view.rows, nodes);
  for (const key of view.items.keys()) if (!usedItems.has(key)) view.items.delete(key);
  return view.fold;
}

function setResponseSummaryWithDuration(head, summary, open, rowCount) {
  let sentence = head.querySelector(".chat-step-sentence");
  if (!sentence) {
    head.textContent = "";
    sentence = document.createElement("span");
    sentence.className = "chat-step-sentence";
    const seat = document.createElement("span");
    seat.className = "seat";
    seat.style.setProperty("--seat", "9ch");
    const caret = document.createElement("span");
    caret.className = "chat-step-caret";
    head.append(sentence, seat, caret);
  }
  const seat = head.querySelector(".seat");
  const caret = head.querySelector(".chat-step-caret");
  const duration = summary.duration ? ` · ${formatDuration(summary.duration)}` : "";
  if (seat.textContent !== duration) seat.textContent = duration;
  if (caret.textContent !== (open ? "▾" : "›")) caret.textContent = open ? "▾" : "›";
  const draw = (clauses, remainder = true) => {
    const value = responseSummarySentence(summary, clauses, remainder, rowCount);
    if (sentence.textContent !== value) sentence.textContent = value;
  };
  draw(4);
  const fit = () => {
    if (!head.isConnected || !sentence.clientWidth) return;
    for (let clauses = 4; clauses >= 2; clauses--) {
      draw(clauses);
      if (sentence.scrollWidth <= sentence.clientWidth) return;
    }
    draw(1, false);
  };
  fit();
  if (!head.isConnected && head.dataset.fitQueued !== "true") {
    head.dataset.fitQueued = "true";
    requestAnimationFrame(() => { head.dataset.fitQueued = "false"; fit(); });
  }
}

// the trailing duration sits in a SEAT rather than in the summary
// string, and the seat is there whether a duration is or not.
//
// A step summary draws its measured duration only when it rounds to at least a
// millisecond, so two runs of one build read "2 tool calls · 2 failed" and "2
// tool calls · 2 failed · 1 ms". That presence difference is what the
// duration-seat MASK existed to cover -- a mask over the place a value would
// sit. The place is real now, so the mask is gone: removes the
// masks whose only job was hiding movement, so that a future difference in
// those pixels is seen rather than covered.
//
// Nine characters, because formatDuration's widest form is "21600.0 s" and
// run.max_wall_clock_seconds is 21600 by default -- the longest run the product
// allows. Sized from the range, per (c), not from today's sample.
function setSummaryWithDuration(head, label, milliseconds) {
  let text = head.firstChild;
  if (!text || text.nodeType !== 3) {
    head.textContent = "";
    text = document.createTextNode("");
    head.append(text);
  }
  if (text.nodeValue !== label) text.nodeValue = label;
  let seat = head.querySelector(".seat");
  if (!seat) {
    seat = document.createElement("span");
    seat.className = "seat";
    seat.style.setProperty("--seat", "9ch");
    head.append(seat);
  }
  const drawn = milliseconds ? ` · ${formatDuration(milliseconds)}` : "";
  if (seat.textContent !== drawn) seat.textContent = drawn;
}

function renderResponseToolGroup(session, view, group, recovered = false) {
  let groupView = view.items.get(group.key);
  if (!groupView) {
    const root = document.createElement("div");
    root.className = "chat-tool-group";
    const head = document.createElement("button");
    head.type = "button";
    head.className = "chat-tool-group-head";
    head.onclick = () => {
      touched.add(group.key);
      expanded.has(group.key) ? expanded.delete(group.key) : expanded.add(group.key);
      render();
    };
    const children = document.createElement("div");
    children.className = "chat-tool-group-calls";
    root.append(head, children);
    groupView = { root, head, children, group: true };
    view.items.set(group.key, groupView);
  }
  const open = expanded.has(group.key);
  const outcome = responseStepOutcome(group.items, { movedPast: recovered });
  const parts = [`${group.tool} ×${group.calls}`];
  if (group.thoughts) parts.push(`+${group.thoughts} ${group.thoughts === 1 ? "thought" : "thoughts"}`);
  if (outcome.retried) parts.push(`${outcome.retried} retried`);
  if (outcome.alarm) parts.push(`${group.failed} failed`);
  toggleClass(groupView.root, "alarm", outcome.alarm);
  setAttribute(groupView.head, "aria-expanded", String(open));
  setSummaryWithDuration(groupView.head, `${open ? "▾" : "▸"} ${parts.join(" · ")}`, group.duration);
  const children = open ? group.items.map((item, index) => {
    try {
      // item.key, unprefixed here on purpose. These items come from
      // responseBlocks, which already gives a thought its `thought:` key in BOTH
      // of its branches now — so prefixing again produced `thought:thought:…`,
      // which matched no view, and a thin thought inside a group rendered CLOSED
      // with its body missing from the transcript. The acceptance gate caught it.
      return renderResponseItem(session, view, item, item?.key || `invalid:group:${index}`, item?.type === "tool", outcome.retried > 0);
    }
    catch (error) {
      return renderFailure(item, error, index, true);
    }
  }) : [];
  reconcileChildren(groupView.children, children);
  return groupView.root;
}

function renderResponseItem(session, view, item, key, forceToolOpen = false, recovered = false) {
  if (!item || typeof item !== "object") throw new Error("entry is missing or is not an object");
  if (!item.key) throw new Error("entry key is missing");
	if (item.type === "tool" && item.name === "todo") {
		let todo = view.items.get(key);
		if (!todo) { const root = document.createElement("div"); root.className = "chat-todo"; todo = { root }; view.items.set(key, todo); }
		const rows = (item.args?.items || []).map((task) => { const row = document.createElement("div"); row.className = `chat-todo-row ${task.status || "pending"}`; row.textContent = `${task.status === "done" ? "✓" : task.status === "doing" ? "→" : "·"} ${task.text || ""}`; return row; });
		reconcileChildren(todo.root, rows); return todo.root;
	}
  let itemView = view.items.get(key);
  if (item.type === "notice") {
    const fingerprint = JSON.stringify(item);
    if (!itemView || itemView.fingerprint !== fingerprint) {
      const notice = noticeContent(session, item, false);
      notice.classList.add("chat-response-notice");
      notice.dataset.entryKey = item.key;
      itemView = { step: notice, fingerprint };
      view.items.set(key, itemView);
    }
    return itemView.step;
  }
    if (!itemView) {
      const step = document.createElement("div");
      step.className = `chat-response-step ${item.type === "tool" || item.type === "streaming-tool" ? "chat-response-tool" : ""}`;
      itemView = { step, answer: null, caret: null, answerText: "" };
      view.items.set(key, itemView);
    }
    setAttribute(itemView.step, "data-entry-key", item.key);
    const stepNodes = [];
    if (item.type === "agent") {
      const tokens = item.reasoningTokens || Math.ceil(Array.from(item.reasoning || "").length / 3.6);
      if (tokens > 0 || !item.done) stepNodes.push(thinking(item, tokens));
      if (item.text) {
        if (!itemView.answer) {
          itemView.answer = document.createElement("div");
          itemView.answer.className = "chat-response-answer";
        }
        if (itemView.answerText !== item.text) renderMarkdown(itemView.answer, item.text);
        itemView.answerText = item.text;
        stepNodes.push(itemView.answer);
      }
      if (showsStreamCaret(item)) {
        if (!itemView.caret) {
          itemView.caret = document.createElement("span");
          itemView.caret.className = "stream-caret";
        }
        stepNodes.push(itemView.caret);
      }
    } else if (item.type === "streaming-tool") {
      if (!itemView.tick) {
        itemView.tick = document.createElement("div");
        itemView.tick.className = "tool-tick chat-streaming-tool";
      }
      const elapsed = Math.max(0, Number(item.last_chunk_at || 0) - Number(item.started_at || 0));
      setText(itemView.tick, `${item.name || "tool call"} · ${formatArgumentBytes(item.argument_bytes)} · ~${format(item.argument_tokens)} tokens · ${formatDuration(elapsed) || "0s"}`);
      stepNodes.push(itemView.tick);
    } else if (item.type === "tool") {
      stepNodes.push(toolTick(item, forceToolOpen, recovered && itemFailed(item)));
    } else {
      throw new Error(`unsupported entry type ${String(item.type || "(missing)")}`);
    }
    reconcileChildren(itemView.step, stepNodes);
    return itemView.step;
}

function renderFailure(entry, error, index, nested) {
  const row = document.createElement(nested ? "div" : "section");
  row.className = nested ? "chat-response-step chat-response-notice chat-render-failure" : "chat-entry chat-notice-row chat-render-failure";
	if (!nested) row.setAttribute("aria-label", agentAuthor(null, entryRole(entry)));
  const key = safeEntryValue(entry, "key");
  if (key) row.dataset.entryKey = key;
  const content = document.createElement("div");
  content.className = "chat-content";
  const label = entryLabel(entry, index);
  const reason = errorReason(error);
  content.textContent = `${label} could not render · ${reason}`;
  row.append(content);
  console.error("chat render failure", label, reason);
  return row;
}

function renderHistoryFailure(error) {
  const row = document.createElement("div");
  row.className = "chat-empty chat-render-failure";
  const reason = errorReason(error);
  row.textContent = `Chat history could not render · ${reason}`;
  console.error("chat render failure", "history", reason);
  return row;
}

function entryLabel(entry, index) {
  if (!entry || typeof entry !== "object") return `entry ${index + 1}`;
  const type = safeEntryValue(entry, "type");
  const key = safeEntryValue(entry, "key");
  if (type === "tool") return `tool ${safeEntryValue(entry, "name") || safeEntryValue(entry, "callID") || key || index + 1}`;
  if (type === "notice") return `notice ${safeEntryValue(safeEntryValue(entry, "event"), "type") || key || index + 1}`;
  return `${type || "entry"} ${key || index + 1}`;
}

function errorReason(error) {
  let value = error;
  try { value = error?.message || error; } catch {}
  try { return String(value || "unknown render failure").replace(/\s+/g, " ").slice(0, 240); }
  catch { return "unknown render failure"; }
}

function safeEntryValue(entry, key) {
  try { return entry && typeof entry === "object" ? entry[key] : undefined; }
  catch { return undefined; }
}

function renderFileChip(session, file) {
  const key = `${session.id}:${file.path.toLowerCase()}:${file.callID}`;
  usedFileViews.add(key);
  let state = fileStates.get(key);
  if (!state) {
    state = { state: "checking", bytes: file.bytes };
    fileStates.set(key, state);
    probeFile(fileURL(session.id, file.path)).then((next) => {
      fileStates.set(key, next);
      schedule();
    });
  }
  const fingerprint = `${state.state}|${state.bytes}|${file.bytes}|${file.path}|${file.callID}|${file.openPath}|${file.openScope}`;
  let view = fileViews.get(key);
  if (view?.fingerprint === fingerprint) return view.node;
  const presentation = attachmentBubblePresentation(session, file);
  const node = createFileChip(document, file, state, {
    openFolder: async () => {
      try {
        await api("/api/open-folder", { session_id: session.id, path: file.openPath, scope: file.openScope });
      } catch (error) {
        localNotice = error.message || String(error);
        localAlarm = true;
        renderComposer(session);
      }
    },
    openFile: async () => {
      try {
        await api("/api/open-file", { session_id: session.id, path: file.openPath, scope: file.openScope });
      } catch (error) {
        localNotice = error.message || String(error);
        localAlarm = true;
        renderComposer(session);
      }
    },
  }, { ...presentation, sessionID: session.id });
  fileViews.set(key, { node, fingerprint });
  return node;
}

function attachmentBubblePresentation(session, file) {
  const note = file.note || file.outcome || attachmentReadability(session, store.connections, file) || "";
  if (file.sidecar) return { state: "sent as text", title: note };
  if (file.outcome || /cannot read/i.test(note)) return { state: "this model cannot read it", alarm: true, title: note };
  return { state: "", title: note };
}

function selectionWithin(node) {
  const selection = document.getSelection?.();
  if (!selection || selection.isCollapsed) return false;
  for (let index = 0; index < selection.rangeCount; index++) if (node.contains(selection.getRangeAt(index).commonAncestorContainer)) return true;
  return false;
}

function thinking(entry, tokens) {
  return thinkingRenderer.render(entry, tokens);
}

function toolTick(entry, forceOpen = false, recovered = false) {
  if (!entry.args || typeof entry.args !== "object" || Array.isArray(entry.args)) throw new Error("tool arguments are missing or are not an object");
  usedToolViews.add(entry.key);
  let view = toolViews.get(entry.key);
  if (!view) {
    const root = document.createElement("div");
    const button = document.createElement("button");
    button.type = "button";
    button.className = "tool-tick";
    button.innerHTML = '<span class="tool-name"></span><span class="tool-key"></span><span class="tool-state"></span><span class="tool-ms"></span>';
    const pre = document.createElement("pre");
    pre.className = "tool-detail";
    const collapse = document.createElement("button");
    collapse.type = "button";
    collapse.className = "collapse-arrow";
    collapse.textContent = "↑";
    collapse.setAttribute("aria-label", `Collapse ${entry.name} tool`);
    collapse.onclick = () => { touched.add(entry.key); expanded.delete(entry.key); render(); };
    button.onclick = () => {
      touched.add(entry.key);
      expanded.has(entry.key) ? expanded.delete(entry.key) : expanded.add(entry.key);
      render();
    };
    root.append(button);
    view = { root, button, pre, collapse, args: null, result: null, content: null };
    toolViews.set(entry.key, view);
  }
  // a harness note is a line of its own, never part of the result.
  const note = typeof entry.result?.harness_note === "string" ? entry.result.harness_note : "";
  if (note) {
    if (!view.note) { view.note = document.createElement("div"); view.note.className = "tool-harness-note"; }
    setText(view.note, `harness · ${note}`);
    if (view.root.firstChild !== view.note) view.root.prepend(view.note);
  } else if (view.note?.isConnected) {
    view.note.remove();
  }
  const open = touched.has(entry.key) ? expanded.has(entry.key) : forceOpen || expanded.has(entry.key);
  setAttribute(view.button, "aria-expanded", String(open));
  const state = entry.result && typeof entry.result.ok === "boolean" ? (entry.result.ok ? "ok" : "error") : "";
  const delegated = entry.name === "delegate" ? entry.result?.delegate : null;
  const shortened = entry.result?.model_shortened ? ` · model kept ${formatBytes(entry.result.model_kept_bytes || 0)}` : "";
  const stateText = (recovered ? "retried" : delegated ? `${entry.result?.delegate_status || state} · ${Number(delegated.tool_calls || 0)} tool calls` : callServiceStatus(entry.name, entry.result) || state) + shortened;
  setText(view.button.children[0], `${open ? "▾" : "▸"} ${entry.name} ·`);
  setText(view.button.children[1], delegated ? delegateKey(entry.args) : shortToolTarget(entry.name, entry.args));
  setText(view.button.children[2], stateText);
  setAttribute(view.button.children[2], "class", `tool-state ${state === "error" && !recovered ? "error" : ""}`);
  setText(view.button.children[3], formatDuration(entry.result?.ms));
  if (open) {
    if (!view.pre.textContent || view.args !== entry.args || view.result !== entry.result || view.content !== entry.content) {
      view.pre.textContent = delegated ? delegateDetail(entry.args, delegated) : `arguments\n${JSON.stringify(entry.args, null, 2)}\n\nresult\n${capResult(entry.content)}`;
    }
    if (!view.collapse.isConnected) view.root.append(view.collapse);
    if (!view.pre.isConnected) view.root.append(view.pre);
  } else if (view.pre.isConnected) {
    view.pre.remove();
    view.collapse.remove();
  }
  view.args = entry.args;
  view.result = entry.result;
  view.content = entry.content;
  return view.root;
}

function setText(node, value) {
  if (node.textContent !== value) node.textContent = value;
}

function setAttribute(node, name, value) {
  if (node.getAttribute(name) !== value) node.setAttribute(name, value);
}

function toggleClass(node, name, enabled) {
  if (node.classList.contains(name) !== enabled) node.classList.toggle(name, enabled);
}

function setProperty(node, name, value) {
  if (node[name] !== value) node[name] = value;
}

function formatBytes(value) {
  const bytes = Math.max(0, Number(value) || 0);
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(bytes < 10 * 1024 * 1024 ? 1 : 0)} MB`;
}

function renderNotice(session, entry) {
  const viewKey = viewKeyFor(entry);
  const fingerprint = JSON.stringify(entry);
  let view = entryViews.get(viewKey);
  if (!view || view.fingerprint !== fingerprint) {
    const row = document.createElement("div");
    row.className = "chat-entry chat-notice-row";
	  const content = noticeContent(session, entry, false);
    if (content.classList.contains("alarm")) row.classList.add("alarm");
    row.append(content);
    view = { row, content, fingerprint };
    entryViews.set(viewKey, view);
  }
  usedEntryViews.add(viewKey);
	setAttribute(view.row, "aria-label", agentAuthor(session, entryRole(entry)));
  const reason = entry.event?.data?.reason;
  setTranscriptCopyRecord(view.row, entry.event?.type === "run.stopped"
    ? `stopped: ${String(reason || "").replaceAll("_", " ")}`
    : `— harness —\n${view.content.innerText || view.content.textContent || entry.text || ""}`);
  return view.row;
}

function noticeContent(session, entry, actionable) {
  const event = entry.event;
  const content = document.createElement("div");
  content.className = "chat-content";
  if (!event || typeof event !== "object") {
		if (typeof entry.text !== "string" || !entry.text.length) throw new Error("notice event is missing or is not an object");
		content.textContent = entry.text;
		return content;
	}
  const data = event.data && typeof event.data === "object" ? event.data : {};
  if (event.type === "run.stopped") {
    const reason = (data.reason || "").replaceAll("_", " ");
		if (data.reason === "model_unreachable") {
			const returned=String(data.detail || "").includes("your message is back in the box"); const line=document.createElement("span"); line.textContent=returned ? "model unreachable · your message is back in the box" : `model unreachable · ${entry.text || session.model_unreachable?.host || "model"}`; if(data.detail) line.title=data.detail; content.append(line);
		} else if (String(data.detail || "").includes("your message is back in the box")) {
			content.textContent = `${reason} · your message is back in the box`;
		} else if (data.reason === "aborted_mid_tool") {
			content.textContent = "stopped mid-tool";
			if (data.detail) content.title = data.detail;
		} else if (data.reason === "reply_empty_reasoning_shown" || data.reason === "reply_empty") {
			content.textContent = data.detail || reason;
		} else if (data.reason === "done") {
			// a run that finished shows only where its time went. "stopped:
			// done" is not news, and this notice was hidden outright until this item gave
			// it something to say. 2iw's rule — the reason first — governs the stops that
			// are worth a reason.
			content.textContent = "";
		} else content.textContent = `stopped: ${reason}${data.detail ? ` · ${data.detail}` : ""}`;
    // where the time went, as a sentence, AFTER the reason — 2iw
    // put the reason first and it stays first. A run journalled before this item
    // carries no time and gets no sentence rather than a row of zeros.
    const summary = runTimeSentence(data);
    if (summary.text) {
      const line = document.createElement("div");
      line.className = "chat-run-time";
      line.textContent = summary.text;
      if (summary.title) line.title = summary.title;
      content.append(line);
    }
    if (data.reason !== "done") content.classList.add("alarm");
  } else if (event.type === "c.job") {
    content.textContent = data.question || "the worker asked a question";
    if (data.item) content.title = `item ${data.item}`;
  } else if (event.type === "files.delivered") {
    const items = data.items || [];
    if (!items.length) content.hidden = true;
    else {
      const copied = items.filter((item) => item.status === "copied").length;
      const identical = items.filter((item) => item.status === "identical").length;
      const failed = items.filter((item) => item.status === "failed").length;
      const parts = [];
      if (copied) parts.push(`${copied} copied to ${data.exchange_folder}`);
      if (identical) parts.push(`${identical} identical skipped`);
      if (failed) parts.push(`${failed} failed`);
      const summary = document.createElement("div");
      summary.textContent = `delivery: ${parts.join(" · ")}`;
      content.append(summary);
      for (const item of items) {
        const path = document.createElement("div");
        path.className = "mono";
        path.textContent = `${item.source_path || "file"}${item.delivered_path ? ` → ${item.delivered_path}` : ""}`;
        content.append(path);
      }
      if (failed) content.classList.add("alarm");
    }
  } else if (event.type === "run.queued") content.textContent = data.behind ? `waiting for model · behind ${data.behind}` : `waiting for a slot (position ${data.position})`;
  else if (event.type === "run.aborted") {
    const pathCount = Array.isArray(data.possibly_written_paths) ? data.possibly_written_paths.length : 0;
    content.textContent = `harness: ${String(data.reason || "aborted").replaceAll("_", " ")} at turn ${data.turn || 0}${pathCount ? ` · ${pathCount} possibly partial path${pathCount === 1 ? "" : "s"} unverified` : ""}`;
    content.classList.add("alarm");
  }
  else if (event.type === "message.queued") content.textContent = `queued (${data.position})`;
  else if (event.type === "model.retry") content.textContent = data.reason === "truncated_tool_call"
    ? `harness: retrying truncated ${data.tool || "tool"} call (${data.attempt || 1}/${data.max_attempts || 1})`
    : `harness: repaired malformed ${data.tool || "tool"} history and retried`;
  else if (event.type === "compaction") content.textContent = data.trigger === "byte_limit_trim"
    ? `trimmed ${data.trimmed_results || 0} old result(s) · removed ${formatBytes(data.bytes_removed || 0)} · server size limit`
    : data.kind === "fresh"
      ? data.fallback_reason
        ? `Earlier work was put aside without a summary because of ${data.fallback_reason}; the record can be searched with chat_history.`
        : `fresh context · ${data.before || 0} → ${data.after || 0} tokens · ${{ byte_limit: "server size limit", message_limit: "server message limit", server_window_refusal: "server refused the window", server_refusal: "server refused the request" }[data.trigger] || "window full"} · earlier turns searchable`
	      : `compacted ${signed((data.after || 0) - (data.before || 0))} tokens · window full · older tool output and file-write bodies stubbed${data.connection_id ? ` via ${data.connection_id}` : ""}`;
  if (event.type === "compaction" && data.trigger !== "byte_limit_trim" && data.before === data.after) content.hidden = true;
  else if (event.type === "workspace.conflict") {
    content.textContent = `conflict: ${data.path} written by ${data.other_label} ${data.age_s} s ago`;
    content.classList.add("alarm");
  } else if (event.type === "memory.noted") {
    // the row says WHICH KIND of note and whether it replaced one,
    // because "noted for next session" told the user nothing about what the
    // agent now believes. An untrusted turn is marked, since that is the note
    // worth a second look.
    const scope = data.scope ? ` · ${data.scope}` : "";
    const replaced = data.replaced ? " · replaced one" : "";
    const beside = data.untrusted_in_turn ? " · written beside untrusted content" : "";
    content.textContent = `noted for next session${scope}${replaced}${beside}`;
    if (data.untrusted_in_turn) content.classList.add("alarm");
  }
  else if (event.type === "operator.context") {
    const entry = operatorLogEntry(data);
    content.textContent = entry.text;
    if (entry.alarm) content.classList.add("operator-mode-enabled");
  }
	else if (event.type === "shell.grant") {
		const executable = data.executable ? ` · ${data.executable}` : "";
		content.textContent = `shell grant: ${String(data.rule || "shell").replaceAll("_", " ")} · ${data.identity || "service"} · for this ${data.scope === "session" ? "chat" : "run"}${executable}`;
	}
  else if (event.type === "shell.grant_lapsed") {
    const executable = data.executable ? ` · ${data.executable}` : "";
		content.textContent = `shell grant lapsed: ${String(data.rule || "shell").replaceAll("_", " ")}${executable}`;
	}
	else if (event.type === "file.grant") content.textContent = `file-tool grant: Run as you · for this ${data.scope === "session" ? "chat" : "run"}`;
	else if (event.type === "file.grant_lapsed") content.textContent = `file-tool grant lapsed: ${data.scope === "session" ? "chat ended" : "run ended"}`;
	else if (event.type === "service.identity_unavailable") {
		content.textContent = data.message || "Agent_b sets up its service identity now; Windows will ask once";
		content.classList.add("alarm");
		const actions = Array.isArray(data.actions) ? data.actions : [data.action];
		if (actions.includes("provision") && !store.replay) {
			const action = document.createElement("button");
			action.type = "button";
			action.textContent = "Set up now (one Windows prompt)";
			action.onclick = async () => {
				action.disabled = true;
				try {
					const result = await api("/api/service-account", {
						action: "provision", connection_id: session.connection_id,
						allow_local_network: !!store.config.shell?.allow_local_network,
						local_subnets: store.config.shell?.confirmed_local_subnets || [],
					});
					content.textContent = result.message || "service identity set up";
					content.classList.remove("alarm");
				} catch (error) {
					content.textContent = `service identity not set up: ${error.message}. Next: open Settings > Security and choose Set up service identity.`;
				}
			};
			content.append(" ", action);
		}
		if (actions.includes("operator_mode") && !store.replay) {
			const action = document.createElement("button");
			action.type = "button";
			action.textContent = "Run as you for 20 minutes";
			action.onclick = async () => {
				action.disabled = true;
				try {
					await api("/api/config", { shell: { operator_context: true } });
					content.textContent = "Run as you enabled for 20 minutes";
					content.classList.remove("alarm");
				} catch (error) {
					action.disabled = false;
					content.prepend(`run-as-you was not enabled: ${error.message}. `);
				}
			};
			content.append(" ", action);
		}
	}
	else if (event.type === "approval.required") {
		return createApprovalCard(document, entry, {
			replay: store.replay,
			decide: actionable ? (callID, decision, answer) => api("/api/approve", { session_id: session.id, call_id: callID, decision, answer }) : null,
		});
  }
  if (entryRole(entry) === "c") {
    const label = document.createElement("span");
    label.className = "chat-notice-author";
    label.textContent = agentAuthor(session, "c");
    content.prepend(label);
  }
  return content;
}

// the microphone. Trace while it listens, Mute at rest, and the
// hover text says whether the recogniser is running offline on this host or
// through the online path. The operator asked for the Windows recogniser -
// the one behind Win+H - so there is no browser speech API here and no audio
// leaves the machine by any path Windows itself does not take.
let micState = { listening: false, available: null, offline: null, reason: "" };
// a mic failure belongs on the strip, not only in a hover title.
let micNotice = "";

function renderMic(session) {
  if (!mic) return;
  const usable = !!session && !store.replay && micState.available !== false;
  setProperty(mic, "disabled", !usable);
  setAttribute(mic, "data-state", micState.listening ? "listening" : "idle");
  mic.classList.toggle("listening", micState.listening);
  // the hover says which engine is listening and whether anything
  // would leave the machine if it did. The host answers that; this draws the
  // answer rather than deciding it.
  const where = micState.available === false
    ? (micState.reason || "dictation is unavailable on this host")
    : (micState.reason || (micState.offline === false ? "online" : "offline"));
  const label = micState.listening ? "Stop dictating" : "Dictate";
  setAttribute(mic, "aria-label", label);
  setAttribute(mic, "title", label + " \u00b7 " + where);
}

// The host finding is asked for once, and never blocks the composer: an
// unreachable route leaves the mic disabled with the reason on hover.
async function loadMicAvailability() {
  if (!mic) return;
  try {
    const status = await api("/api/speech", undefined, "GET");
    micState.available = !!status?.available;
    micState.offline = status?.offline !== false;
    micState.reason = String(status?.reason || "");
  } catch (error) {
    micState.available = false;
    micState.reason = error.message;
  }
  renderMic(store.sessions[selectedID()]);
}

function renderComposer(session) {
  renderChatProposals(chatProposals, session, input);
	document.body.classList.toggle("no-open-chats", !session);
	document.body.classList.toggle("run-active", isRunning(session));
  const phoneOwned = session?.origin === "phone" && session?.owner === "phone";
  send.disabled = !!store.replay || phoneOwned;
  input.disabled = !!store.replay || phoneOwned;
  attachButton.disabled = !session || !!store.replay || attachmentsBusy || phoneOwned;
  input.removeAttribute("placeholder");
  const queued = session?.queued_messages || 0;
  // a run queued behind its model connection says whose run it waits on.
  const queuedBehind = session?.run?.status === "queued" && session.run.waiting_behind ? `waiting for model · behind ${session.run.waiting_behind}` : "";
  const state = session?.pending_approval || session?.pending_repo_policy ? "waiting for you" : queuedBehind || session?.run?.status || "idle";
  const unreachable = session?.model_unreachable;
  const busy = session?.model_busy;
  const operatorUntil = store.shell_identity?.operator_context ? `Run as you · until ${shortTime(store.shell_identity.operator_context_expires_at)}` : "";
  const queueText = queued ? `queued (${queued})${unreachable ? " · waiting for model" : ""}` : "";
  const activity = liveActivityText(session, Date.now());
  // A reachable open request says what it is doing and how much it has done;
  // the old sticky "model busy" condition is never the whole status line.
  const modelLine = unreachable ? "model unreachable" : "";
  const busyLine = activity || (busy ? "waiting for model" : "");
  const primary = modelLine || busyLine || (session && !session.runnable ? session.not_runnable_reason : state);
  const message = localNotice || micNotice || (phoneOwned ? "from phone · read-only while the phone owns this chat" : modelLine && session?.runnable !== false ? modelLine : [primary, queueText, operatorUntil].filter(Boolean).join(" · "));
  // Live state, not decoration: the robot runs beside the live line for exactly
  // as long as the run is live, and is absent otherwise. Its eyes take the same
  // state colour the tab robot uses.
  const running = !!activity;
  // THE ONE WAITING ELEMENT, EARNING ITS PLACE.
  //
  // The model wait is the case the user described — "chatting and hoping the
  // model catches it" — so this is where it goes first. It appears only while a
  // run is live and nothing has been written back yet, which is exactly the
  // interval where the product previously showed a blinking glyph and a sentence
  // and nothing about progress.
  //
  // Determinate when llama.cpp is streaming prompt_progress, which
  // confirmed is the only progress any server here reports;
  // indeterminate for everything else, cycling in place rather than sweeping
  // toward a finish it does not know.
  const progress = session?.activity?.progress || {};
  const promptOnly = running && /^prompt /.test(activity);
  if (!composerWait) composerWait = waitElement(document, { line: "waiting for the model" });
  if (!composerRobot) {
    composerRobot = createAgentRobot(document, { place: "chat" });
  }
  if (promptOnly) {
    const line = unreachable ? "waiting for the model to come back" : activity;
    setText(composerWait.querySelector(".wait-line"), line);
    setWaitProgress(composerWait, {
      processed: progress.processed ?? null,
      total: progress.total ?? null,
    });
  }
  setProperty(composerWait, "hidden", !promptOnly);
  if (running && !promptOnly) {
    updateAgentRobot(composerRobot, {
      state: session?.model_unreachable ? "offline" : session?.pending_approval || session?.pending_repo_policy ? "waiting" : "running",
      doing: liveActivityLook(session),
    });
  }
  setProperty(composerRobot, "hidden", !running || promptOnly);
  if (!composerText) {
    composerText = document.createElement("span");
    composerText.className = "chat-notice-text";
  }
  setText(composerText, message);
  const title = modelLine && message === modelLine ? unreachable?.host || "" : "";
  setAttribute(composerText, "title", title);
  if (!mirrorTakeButton) {
    mirrorTakeButton = document.createElement("button"); mirrorTakeButton.type = "button"; mirrorTakeButton.textContent = "Continue here";
    mirrorTakeButton.onclick = () => void takeMirroredChat();
  }
  setProperty(mirrorTakeButton, "hidden", !phoneOwned);
  setProperty(mirrorTakeButton, "disabled", !phoneOwned || store.replay);
  reconcileChildren(notice, [composerWait, composerRobot, composerText, mirrorTakeButton]);
  setAttribute(notice, "class", `chat-notice ${localAlarm || micNotice || unreachable || (session && !session.runnable) ? "alarm" : ""}`);
  const showStatus = !!session && (isRunning(session) || message !== "idle");
  setProperty(statusLine, "hidden", !showStatus);
  if (showStatus && (statusLine.parentElement !== log || log.lastElementChild !== statusLine)) log.append(statusLine);
  else if (!showStatus && statusLine.isConnected) statusLine.remove();
  const fileNodes = queuedAttachments.map((file) => {
    const key = `${file.path}|${file.bytes}|${file.reused}|${file.sidecar}|${file.tier}`;
    let row = composerFileViews.get(key);
    if (row) return row;
    const presentation = attachmentBubblePresentation(session, file);
    row = createAttachmentBubble(document, file, { ...presentation, sessionID: session.id, removable: true }, { remove: () => removeQueuedAttachment(file) });
    row.classList.add("chat-pending-file");
    composerFileViews.set(key, row);
    return row;
	});
	const upload = activeUpload;
	if (upload && session && upload.sessionID === session.id) {
		const row = createAttachmentBubble(document, upload, { previewURL: upload.previewURL, state: upload.total ? `page ${upload.page} of ${upload.total}` : "", removable: !!upload.total }, { remove: () => void removeActiveAttachment(upload) }); row.classList.add("chat-pending-file");
		fileNodes.unshift(row);
	}
  reconcileChildren(pendingFiles, fileNodes);
	for (const [key, row] of composerFileViews) if (!fileNodes.includes(row)) composerFileViews.delete(key);
	const worker = workerApproval(store.sessions, session);
	setProperty(pendingApproval, "hidden", !(session?.pending_approval || session?.pending_repo_policy || worker));
	const approvalNodes = [];
	if (session?.pending_approval) approvalNodes.push(cachedComposerApproval(`session:${session.id}`, session.pending_approval, () => createApprovalCard(document, session.pending_approval, {
		replay: store.replay,
		decide: (callID, decision, answer) => api("/api/approve", { session_id: session.id, call_id: callID, decision, answer }),
	})));
	else if (session?.pending_repo_policy) approvalNodes.push(cachedComposerApproval(`policy:${session.id}`, session.pending_repo_policy, () => createPolicyCard(session)));
	if (worker) approvalNodes.push(cachedComposerApproval(`worker:${worker.id}`, worker.pending_approval, () => createApprovalCard(document, worker.pending_approval, {
		replay: store.replay,
		author: agentAuthor(worker, "c"),
		decide: (callID, decision, answer) => api("/api/approve", { session_id: worker.id, call_id: callID, decision, answer }),
	})));
	reconcileChildren(pendingApproval, approvalNodes);
	for (const key of composerApprovalViews.keys()) if (!approvalNodes.includes(composerApprovalViews.get(key).node)) composerApprovalViews.delete(key);
  renderSendStop(send, session, store.replay);
  renderMic(session);
  setProperty(retryModel, "hidden", !unreachable);
  setProperty(retryModel, "disabled", !session || store.replay);
}

async function takeMirroredChat() {
  const session = store.sessions[selectedID()];
  if (!session || session.owner !== "phone") return;
  try { await api("/api/chat-mirror/take", { chat_id: session.id }); localNotice = "Continuing on this PC"; localAlarm = false; }
  catch (error) { localNotice = error.message || String(error); localAlarm = true; }
  renderComposer(store.sessions[selectedID()]);
}

function cachedComposerApproval(key, value, create) {
  const fingerprint = JSON.stringify(value);
  let view = composerApprovalViews.get(key);
  if (!view || view.fingerprint !== fingerprint) {
    view = { node: create(), fingerprint };
    composerApprovalViews.set(key, view);
  }
  return view.node;
}

function createPolicyCard(session) {
	const policy = session.pending_repo_policy;
	const card = document.createElement("section"); card.className = "approval-card repo-policy-card";
	const title = document.createElement("span"); title.textContent = "Allow this";
	const heading = document.createElement("strong"); heading.textContent = "Trust this repo's policy?";
	const reason = document.createElement("span"); reason.textContent = "This repository wants to change defaults for this chat.";
	const detail = document.createElement("details"); const summary=document.createElement("summary");summary.textContent="Technical detail";
	const path = document.createElement("span"); path.textContent = `${policy.path}${policy.changed ? " · changed" : ""}`;
	const pre = document.createElement("pre"); pre.textContent = policy.error || `${policy.content}${policy.diff ? `\n\n${policy.diff}` : ""}`; detail.append(summary,path,pre);
	const actions = document.createElement("div"); actions.className = "approval-actions";
	for (const [label, action] of [["Yes, for this chat","policy-approve"],["Just once","policy-once"],["No","policy-deny"]]) { const button=document.createElement("button");button.type="button";button.textContent=label;if(action==="policy-approve"){button.className="default";button.autofocus=true}button.disabled=!!store.replay || (action!=="policy-deny" && !!policy.error);button.onclick=()=>void decidePolicy(session,action);actions.append(button) }
	card.append(title,heading,reason,detail,actions); return card;
}

async function decidePolicy(session, action) {
	const policy=session.pending_repo_policy; if(!policy||store.replay)return;
	try { await api(`/api/workspaces/${action}`,{dir:session.workspace_dir||session.workspace,hash:policy.hash,session_id:session.id}); reduce({type:"snapshot",data:await api("/api/state",undefined,"GET")}); localNotice="";localAlarm=false;render() }
	catch(error){localNotice=error.message||String(error);localAlarm=true;renderComposer(store.sessions[selectedID()])}
}

async function submit() {
  if (micState.listening) stopDictation();
  let session = store.sessions[selectedID()];
  if (!session && !store.replay) session = await shell?.newChat();
  if (!session || store.replay) return;
  const text = input.value.trim();
  if (!text && !queuedAttachments.length) return;
  try {
		await api("/api/message", { session_id: session.id, text, attachments: queuedAttachments.map(attachmentMetadata) });
    input.value = "";
    queuedAttachments = [];
    attachmentQueues.set(selectedID(), queuedAttachments);
    resize();
		localNotice = "";
    localAlarm = false;
  } catch (error) {
    localNotice = error.message;
    localAlarm = true;
  }
  renderComposer(session);
}

async function queueFiles(files) {
  const session = store.sessions[selectedID()];
  if (!session || store.replay || !files.length) return;
  attachmentsBusy = true;
  localNotice = "Uploading attachment…";
  localAlarm = false;
  renderComposer(session);
  try {
    for (const file of files) {
      const uploadID = globalThis.crypto.randomUUID();
	  const image = file.type?.startsWith("image/") ? await imageInfo(file) : {};
	  const upload = activeUpload = { id: uploadID, sessionID: session.id, name: file.name || "attachment", display_name: file.name ? "" : "Pasted image", kind: image.previewURL ? "image" : file.name?.toLowerCase().endsWith(".pdf") ? "pdf" : "binary", page: 0, total: 0, ...image };
	  renderComposer(session);
	  try {
        const uploaded = await uploadAttachment(file, session.id, { token: store.mutation_token, uploadID });
		const cancelled = cancelledUploads.delete(uploadID);
		if (!cancelled && !queuedAttachments.some((item) => item.path.toLowerCase() === uploaded.path.toLowerCase())) queuedAttachments.push({ ...uploaded, display_name: upload.display_name, width: upload.width, height: upload.height });
        if (!cancelled) localNotice = "Attachment ready";
	  } catch (error) {
		if (!cancelledUploads.delete(uploadID)) throw error;
	  } finally {
		if (upload.previewURL) URL.revokeObjectURL(upload.previewURL);
		if (activeUpload === upload) activeUpload = null;
	  }
    }
  } catch (error) {
    localNotice = error.message || String(error);
    localAlarm = true;
  } finally {
	activeUpload = null;
    attachmentsBusy = false;
    renderComposer(session);
  }
}

function removeQueuedAttachment(file) {
	queuedAttachments = queuedAttachments.filter((item) => item !== file);
	attachmentQueues.set(selectedID(), queuedAttachments); renderComposer(store.sessions[selectedID()]);
}

async function removeActiveAttachment(upload) {
	if (activeUpload !== upload || !upload.total) return;
	cancelledUploads.add(upload.id); activeUpload = null; renderComposer(store.sessions[upload.sessionID]);
	try { await api("/api/attachments/stop", { upload_id: upload.id }); }
	catch {}
	renderComposer(store.sessions[selectedID()]);
}

async function imageInfo(file) {
	const previewURL = URL.createObjectURL(file);
	try { const bitmap = await globalThis.createImageBitmap(file); const value = { previewURL, width: bitmap.width, height: bitmap.height }; bitmap.close(); return value; }
	catch { return { previewURL }; }
}

async function refreshExchangeFiles() {
  if (store.replay || !store.sessions[selectedID()]) return;
  try {
    const source = await exchangeFiles();
    const files = source.files;
    const parts = String(source.folder || "").split(/[\\/]/).filter(Boolean);
    attachmentFolder = { name: parts.at(-1) || "attachments", path: source.folder || "" };
    attachExchange.textContent = `From the ${attachmentFolder.name} folder`;
    attachExchange.title = attachmentFolder.path;
    exchangeFileList.replaceChildren(...files.map((file) => {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = `${file.path} · ${format(file.bytes)} B`;
      button.onclick = () => void queueExchangeFile(file);
      return button;
    }));
    if (!files.length) {
      const empty = document.createElement("span");
      empty.textContent = `The ${attachmentFolder.name} folder is empty`;
      exchangeFileList.append(empty);
    }
  } catch (error) {
    localNotice = error.message || String(error);
    localAlarm = true;
    renderComposer(store.sessions[selectedID()]);
  }
}

async function queueExchangeFile(item) {
  const session = store.sessions[selectedID()];
  if (!session || !item) return;
  const uploadID = globalThis.crypto.randomUUID();
  activeUpload = { id: uploadID, sessionID: session.id, name: item.path, page: 0, total: 0, stopping: false };
  attachmentsBusy = true;
  localNotice = `Copying from the ${attachmentFolder.name} folder…`;
  localAlarm = false;
  renderComposer(session);
  try {
    const uploaded = await exchangeUpload(item, session.id, { token: store.mutation_token, uploadID, maxBytes: store.config.tools?.attachments?.max_bytes });
    if (!queuedAttachments.some((value) => value.path.toLowerCase() === uploaded.path.toLowerCase())) queuedAttachments.push(uploaded);
    localNotice = "Attachment ready";
    attachMenu.hidden = true;
  } catch (error) {
    localNotice = error.message || String(error);
    localAlarm = true;
  } finally {
    activeUpload = null;
    attachmentsBusy = false;
    renderComposer(session);
  }
}

// click to dictate, click again to stop. Recognised text lands in
// the composer as it arrives, so the operator watches it appear rather than
// waiting for a block at the end; ten seconds of silence, a second click or a
// send all end the session. Nothing is sent anywhere: the recogniser runs on
// this machine through the harness, and the text goes into the textarea the
// operator is already looking at.
let micStream = null;

function stopDictation() {
  if (micStream) {
    micStream.close();
    micStream = null;
  }
  micState.listening = false;
  renderMic(store.sessions[selectedID()]);
  void api("/api/speech/stop", {}).catch(() => {});
}

function startDictation() {
  const session = store.sessions[selectedID()];
  if (!session || store.replay || micState.available === false) return;
  micState.listening = true;
  micNotice = "";
  renderMic(session);
  // The transcript arrives on the existing event stream shape: one line per
  // partial, the final one marked. A stream that fails leaves the mic idle
  // with the reason on hover rather than a dialog.
  let committed = input.value;
  micStream = new EventSource("/api/speech/stream");
  micStream.onmessage = (event) => {
    let payload = null;
    try { payload = JSON.parse(event.data); } catch { return; }
    if (payload.error) {
      micState.available = false;
      micState.reason = String(payload.error);
      micNotice = `mic: ${micState.reason}`;
      stopDictation();
      renderComposer(store.sessions[selectedID()]);
      return;
    }
    // The helper writes one line per hypothesis and one per utterance. A
    // hypothesis is shown but not kept, so the words change as they are heard;
    // an utterance is committed and the next hypothesis builds on it.
    const partial = typeof payload.partial === "string" ? payload.partial : "";
    const final = typeof payload.final === "string" ? payload.final : "";
    const heard = final || partial;
    if (heard) {
      const joined = (committed ? committed.replace(/\s*$/, "") + " " : "") + heard;
      if (final) committed = joined;
      input.value = joined;
    }
    resize();
    if (payload.done) stopDictation();
  };
  micStream.onerror = () => {
    if (micState.listening) micNotice = "mic: connection to the local dictation helper ended";
    stopDictation();
    renderComposer(store.sessions[selectedID()]);
  };
}

if (mic) {
  mic.onclick = () => {
    if (micState.listening) stopDictation();
    else startDictation();
  };
}

send.onclick = () => {
  const session = store.sessions[selectedID()];
  // While a run is live this control is Stop; idle, it sends. 2fg hold
  // rules are untouched: a message typed during a run still queues behind it.
  if (send.dataset.mode === "stop") return void stopRun();
  void submit();
};

function stopRun() {
  const session = store.sessions[selectedID()];
  if (session && !store.replay) api("/api/stop", { session_id: session.id }).catch((error) => { localNotice = error.message; localAlarm = true; renderComposer(session); });
};
retryModel.onclick = async () => {
  const session = store.sessions[selectedID()];
  if (!session || store.replay) return;
  try {
    const result = await api(`/api/connections/${encodeURIComponent(session.connection_id || session.b_connection)}/probe`, {});
    if (result.status === "passed") {
      reduce({ type: "snapshot", data: await api(`/api/state?session=${encodeURIComponent(session.id)}`, undefined, "GET") });
    }
    render();
  }
  catch {}
};
attachButton.onclick = () => { attachMenuControl.toggle(); if (!attachMenu.hidden) void refreshExchangeFiles(); };
attachBrowse.onclick = () => { attachMenu.hidden = true; filePicker.click(); };
attachExchange.dataset.menuReveal = "";
attachExchange.onclick = () => void refreshExchangeFiles();
filePicker.addEventListener("change", () => {
  void queueFiles([...filePicker.files]);
  filePicker.value = "";
});
document.body.addEventListener("dragenter", (event) => {
  if (mounted && !store.replay && event.dataTransfer?.types?.includes("Files")) {
    event.preventDefault();
    dragDepth++;
    document.body.classList.add("drop-target");
  }
});
document.body.addEventListener("dragover", (event) => {
  if (mounted && !store.replay && event.dataTransfer?.types?.includes("Files")) event.preventDefault();
});
document.body.addEventListener("dragleave", () => {
  dragDepth = Math.max(0, dragDepth - 1);
  if (!dragDepth) document.body.classList.remove("drop-target");
});
document.body.addEventListener("drop", (event) => {
  dragDepth = 0;
  document.body.classList.remove("drop-target");
  if (mounted && !store.replay && event.dataTransfer?.files?.length) {
    event.preventDefault();
    void queueFiles([...event.dataTransfer.files]);
  }
});
installComposerResize({ handle: resizeHandle, composer, input, log });

input.addEventListener("paste", (event) => {
  const files = [...(event.clipboardData?.files || [])];
  if (files.length) {
    event.preventDefault();
    void queueFiles(files);
  }
});
input.addEventListener("keydown", (event) => {
  if (event.key === "Enter" && !event.shiftKey) {
    event.preventDefault();
    submit();
  } else if (event.key === "Escape") {
    input.value = "";
    resize();
  }
});
let loadingEarlier = false;
log.addEventListener("scroll", () => {
  follow = log.scrollHeight - log.clientHeight - log.scrollTop <= 24;
	const session = store.sessions[selectedID()];
	if (log.scrollTop <= 24 && Number(session?.history_start || 0) > 0 && !loadingEarlier) {
		loadingEarlier = true;
		const height = log.scrollHeight;
		void loadSessionHistory(session.id, true).then(() => requestAnimationFrame(() => { log.scrollTop += log.scrollHeight - height; })).finally(() => { loadingEarlier = false; });
	}
});
document.addEventListener("keydown", (event) => {
  if (!mounted) return;
  if (event.ctrlKey && event.key === ".") {
    event.preventDefault();
    document.getElementById("shell-stop")?.click();
  } else if (event.key === "/" && !/INPUT|TEXTAREA|SELECT/.test(document.activeElement?.tagName || "")) {
    event.preventDefault();
    input.focus();
  }
});

// the composer's height is the dragged one now, so there is nothing
// for this to step through.
function resize() {}
function busy(session) {
  return !!session && ["running", "queued", "paused", "stopping"].includes(session.run?.status);
}
function delegateKey(args) {
  return String(args?.task || "").trim().split(/\s+/).slice(0, 8).join(" ");
}
function delegateDetail(args, delegated) {
  const transcript = Array.isArray(delegated.transcript) ? delegated.transcript.map((message) => {
    const parts = [`${message.role || "message"}${message.name ? ` ${message.name}` : ""}`];
    if (message.reasoning) parts.push(`thought\n${message.reasoning}`);
    if (message.tool_calls?.length) parts.push(`calls\n${JSON.stringify(message.tool_calls, null, 2)}`);
    if (message.content) parts.push(String(message.content));
    return parts.join("\n");
  }).join("\n\n") : "";
  const outcome = [delegated.connection ? `connection: ${delegated.connection}` : "", delegated.applied?.length ? `applied: ${delegated.applied.join(", ")}` : "", delegated.collided?.length ? `collided: ${delegated.collided.join(", ")}` : "", delegated.scratch ? `helper versions: ${delegated.scratch}` : ""].filter(Boolean).join("\n");
  return `arguments\n${JSON.stringify(args, null, 2)}${outcome ? `\n\nresult\n${outcome}` : ""}\n\nchild transcript\n${transcript}\n\nsummary\n${delegated.summary || ""}`;
}
function capResult(value) {
  const lines = String(value || "").split("\n");
  return lines.length <= 200 ? lines.join("\n") : [...lines.slice(0, 199), "[… open in timeline for the rest]"].join("\n");
}
function signed(value) {
  return `${value < 0 ? "−" : value > 0 ? "+" : "±"}${format(Math.abs(value))}`;
}
function format(value) {
  return Number(value || 0).toLocaleString("en-US");
}
function formatArgumentBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  return bytes < 1000 ? `${bytes} B` : `${(bytes / 1000).toFixed(bytes < 10000 ? 1 : 0)} kB`;
}
function shortTime(value) {
  const date = new Date(value || "");
  return Number.isNaN(date.getTime()) ? "soon" : date.toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"});
}
