import { api, reduce, setSelection, store, subscribe } from "./bus.js";
import { renderMarkdown } from "./markdown.js";
import { operatorLogEntry } from "./operator-log.js";
import { createThinkingRenderer } from "./reasoning.js";
import { formatDuration } from "./duration.js";
import { createFileChip, fileURL, filesFromResponse, probeFile } from "./deliverables.js";
import { createApprovalCard } from "./approval.js";
import { callServiceKey, callServiceStatus } from "./call-service-display.js";
import { attachmentChipFile, attachmentMetadata, exchangeFiles, exchangeUpload, uploadAttachment } from "./attachment-upload.js";
import { attachmentReadability } from "./attachment-readability.js";
import { agentAuthor, isRunning, openSessions, sameWorkerPlan, workerApproval } from "./chat-lifecycle.js";
import { renderSendStop } from "./stop-state.js";
import { groupResponseRows, hasVisibleChatContent, isHeaderlessSteps, isIdenticalSingleStepFold, itemFailed, responseBlocks, responseSummary } from "./chat-response-groups.js";
import { navigationSurfaceReady } from "./navigation-telemetry.js";
import { liveActivityText, showsStreamCaret } from "./chat-activity.js";
import { compactionFigures } from "./panel-lifetime.js";
import { renderChatProposals } from "./chat-proposals.js";
import { installTranscriptCopy } from "./transcript-copy.js";

const budget = document.getElementById("chat-budget");
const readout = document.getElementById("chat-readout");
const readoutFigures = document.getElementById("chat-readout-figures");
const readoutStop = document.getElementById("chat-readout-stop");
const readoutLabel = document.getElementById("chat-readout-label");
const log = document.getElementById("chat-log");
const input = document.getElementById("chat-task");
const expandComposer = document.getElementById("chat-expand");
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
const filePicker = document.getElementById("chat-file-picker");
const pendingFiles = document.getElementById("chat-attachments");
// Item 2ge: send and stop are one control, so there is one element for both.
const mic = document.getElementById("chat-mic");
const retryModel = document.getElementById("chat-retry-model");
let requested = new URLSearchParams(location.search).get("session");
// Item 2gn: the chat the operator asked for by name, so the stale-selection
// sweep never takes him off a closed chat he opened deliberately.
let askedFor = "";
const selectedID = () => store.selection.session_id;
// Item 2gg: copying the transcript marks the kind of each entry. Installed
// once, on the document, and inert unless the selection is in the transcript.
installTranscriptCopy(log);
const expanded = new Set();
let follow = true;
let page = 0;
let localNotice = "";
let localAlarm = false;
let frame = 0;
let renderTimer = 0;
let attachmentsBusy = false;
let dragDepth = 0;
let queuedAttachments = [];
let composerExpanded = false;
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
  formatDuration: formatThoughtSeconds,
  uncounted: () => {
    const session = store.sessions[selectedID()];
    const profile = store.servers.find((value) => value.id === (session?.server_id || session?.b_profile));
    return profile?.capabilities?.reasoning_emission === "inline";
  },
});
const entryViews = new Map();
const fileStates = new Map();
const fileViews = new Map();
const toolViews = new Map();
let usedEntryViews = new Set();
let usedFileViews = new Set();
let usedToolViews = new Set();
const earlierButton = document.createElement("button");
earlierButton.type = "button";
earlierButton.className = "chat-earlier";
earlierButton.onclick = () => {
  page++;
  follow = false;
  renderLog(store.sessions[selectedID()]);
};
const jumpButton = document.createElement("button");
jumpButton.type = "button";
jumpButton.className = "chat-jump";
jumpButton.textContent = "Jump to latest";
jumpButton.onclick = () => {
  follow = true;
  page = 0;
  renderLog(store.sessions[selectedID()]);
};
subscribe((_state, event) => {
  if (event.type === "snapshot") {
    const open = newestOpenSessions();
    // Item 2gn: a link that names a chat opens THAT chat, closed or open. The
    // `!closed` test sent the operator to whatever chat happened to be
    // selected — five of five ids in the v1.1.2 probe — and nine of his ten
    // retained chats are closed, so it was his whole history.
    if (requested && store.sessions[requested]) { askedFor = requested; changeBound(requested); requested = ""; }
    else if (store.selection.agent_id === "agent_b" && (!store.sessions[selectedID()] || store.sessions[selectedID()].closed)) changeBound(open[0]?.id || "");
  }
  // The sweep below exists for a selection that went stale on its own. A chat
  // the operator asked for by name is not stale, so it is left in front of him
  // until he chooses another (item 2gn).
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
  renderBudget(session);
  renderReadout(session);
  renderLog(session);
  renderComposer(session);
  navigationSurfaceReady("chat", store);
}

export function mountChat(shellController) {
  shell = shellController;
  mounted = true;
  void loadMicAvailability();
  schedule();
}

export function unmountChat() {
  mounted = false;
  if (frame) cancelAnimationFrame(frame);
  if (renderTimer) clearTimeout(renderTimer);
  frame = 0;
  renderTimer = 0;
  dragDepth = 0;
  document.body.classList.remove("drop-target");
}

function newestOpenSessions() {
  return openSessions(store.sessions).sort((left, right) => Date.parse(right.created_at || 0) - Date.parse(left.created_at || 0));
}

function renderBudget(session) {
  const value = session?.budget || {};
  const used = value.used_measured || value.used_est || 0;
  const ceiling = value.ceiling || 0;
  const ratio = ceiling ? used / ceiling : 0;
  budget.className = `chat-budget ${ratio > 1 ? "over" : ratio > 0.85 ? "warn" : ""}`;
  budget.querySelector(".chat-budget-fill").style.width = `${Math.min(100, ratio * 100)}%`;
  budget.querySelector(".chat-budget-tip").textContent = `${value.estimated ? "estimated · " : ""}${format(used)} / ${format(ceiling)}`;
}

// Item 2gk: the figures for this chat, collapsed by default, on the chat they
// describe. Compactions, summaries and tokens are always true of the chat; the
// stop reason and the label are true of its last run, so they are the body that
// opens rather than the line that is always read.
function renderReadout(session) {
  readout.hidden = !session;
  if (!session) return;
  const value = session.budget || {};
  const used = value.used_measured || value.used_est || 0;
  const ceiling = value.ceiling || 0;
  const run = session.run || {};
  const ended = !!run.last_stop_reason && !["running", "queued", "stopping"].includes(run.status);
  readoutFigures.textContent = `${compactionFigures(session)} · ${value.estimated ? "estimated " : ""}${format(used)} / ${format(ceiling)} tokens${ended ? ` · ${run.last_stop_reason}` : ""}${ended && run.result_label ? ` · ${run.result_label}` : ""}`;
  readoutStop.textContent = ended
    ? `Ended: ${run.last_stop_reason}${run.last_stop_detail ? ` — ${run.last_stop_detail}` : ""} · armed: ${run.armed_detectors?.length ? run.armed_detectors.join(", ") : "none"}`
    : "No run has ended in this chat yet.";
  readoutLabel.replaceChildren(...(ended ? ["productive", "stuck", "mixed"].map((label) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = run.result_label === label ? "selected" : "";
    button.dataset.label = label;
    button.textContent = label;
    button.disabled = !!store.replay;
    return button;
  }) : []));
}

function renderLog(session) {
  const wasBottom = follow;
  thinkingRenderer.begin();
  usedEntryViews = new Set();
  usedFileViews = new Set();
  usedToolViews = new Set();
  // Item 2ew: before the first snapshot nothing is known, so nothing is claimed.
  if (!store.loaded) {
    finishLogRender([]);
    return;
  }
  if (!session) {
    const empty = document.createElement("div");
    empty.className = "chat-empty";
    const noAgent = !(store.config.agents || []).length;
    const face = document.createElement("img");
    face.src = "/static/assets/operator-off-48.png";
    face.width = 48; face.height = 48; face.alt = "";
    const line = document.createElement("span");
    if (noAgent) {
      line.append("No agent connected — add one in ");
      const settings = document.createElement("a"); settings.textContent = "Settings"; settings.href = "/#settings/servers";
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
    empty.textContent = session.runnable ? "Send a task to start the loop." : session.not_runnable_reason;
    finishLogRender([empty]);
    return;
  }
  const nodes = [];
  const end = Math.max(0, entries.length - page * 100);
  const start = Math.max(0, end - 300);
  if (start > 0) {
    earlierButton.textContent = `earlier: ${start} entries`;
    nodes.push(earlierButton);
  }
  for (const [index, entry] of entries.slice(start, end).entries()) nodes.push(renderEntrySafely(session, entry, start + index));
  jumpButton.hidden = follow && page === 0;
  nodes.push(jumpButton);
  finishLogRender(nodes);
  requestAnimationFrame(() => {
    if (wasBottom && page === 0) log.scrollTop = log.scrollHeight;
    jumpButton.hidden = follow && page === 0;
  });
}

function finishLogRender(nodes) {
  reconcileChildren(log, nodes);
  thinkingRenderer.end();
  for (const key of entryViews.keys()) if (!usedEntryViews.has(key)) entryViews.delete(key);
  for (const key of fileViews.keys()) if (!usedFileViews.has(key)) fileViews.delete(key);
  for (const key of toolViews.keys()) if (!usedToolViews.has(key)) toolViews.delete(key);
}

function reconcileChildren(parent, nodes) {
  for (let index = 0; index < nodes.length; index++) {
    if (parent.children[index] !== nodes[index])
      parent.insertBefore(nodes[index], parent.children[index] || null);
  }
  while (parent.children.length > nodes.length) parent.lastElementChild.remove();
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
  // Item 2fc: "Build plan now?" opens the planning chat with its request in
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
    // A notice that is waiting on the operator is never folded into a steps
    // group: an unreachable model, and a question from the worker, are the two
    // things in this thread that nobody can answer without seeing them. Item
    // 2fg: nor is a run stopped mid-tool, so the transcript shows the stop.
    if (entry.type === "notice" && ((entry.event?.type === "run.stopped" && entry.event?.data?.reason === "model_unreachable") || (entry.event?.type === "run.stopped" && entry.event?.data?.reason === "aborted_mid_tool") || entry.event?.type === "c.job")) {
      grouped.push(entry);
      response = null;
      continue;
    }
    if (!response) {
      response = { type: "response", key: `response:${boundary}`, items: [] };
      grouped.push(response);
    }
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

// Item 2gs (v1.2.1/W1): a key that appears twice in one render gets its own
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
// order. The duplicate ids themselves are a separate defect ([[2fm]]).
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
    const author = speaker(entry.type === "user" ? "you" : entry.type === "summary" ? "summary" : agentAuthor(session, entryRole(entry)), entry.type !== "user" && entry.type !== "summary");
    row.append(author, content);
    view = { row, author, content, text: "" };
    entryViews.set(viewKey, view);
  }
  usedEntryViews.add(viewKey);
  view.row.dataset.entryKey = entry.key;
  view.author.lastElementChild.textContent = entry.type === "user" ? "you" : entry.type === "summary" ? "summary" : agentAuthor(session, entryRole(entry));
  view.row.className = `chat-entry ${entry.type === "user" ? "chat-user" : entry.type === "summary" ? "chat-summary" : entry.type === "tool" ? "tool-entry" : "chat-agent"}`;
  const content = view.content;
  if (entry.type === "user") {
    const nodes = [];
    if (entry.text) {
      if (!view.userText) view.userText = document.createElement("div");
      if (view.text !== entry.text) view.userText.textContent = entry.text;
      nodes.push(view.userText);
    }
    for (const attachment of entry.attachments || []) {
      nodes.push(renderFileChip(session, attachmentChipFile(attachment)));
      if (attachment.outcome) {
        const outcome = document.createElement("div");
        outcome.className = "chat-attachment-warning";
        outcome.textContent = attachment.outcome;
        nodes.push(outcome);
      }
    }
    reconcileChildren(content, nodes);
    view.text = entry.text;
  }
  else if (entry.type === "summary") {
    if (view.text !== entry.text) content.textContent = entry.text || "";
    view.text = entry.text || "";
  }
  else throw new Error(`unsupported top-level entry type ${String(entry.type || "(missing)")}`);
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
    const summary = document.createElement("button");
    summary.type = "button";
    summary.className = "chat-response-summary";
    summary.onclick = () => {
      const allOpen = view.stepKeys.length > 0 && view.stepKeys.every((key) => expanded.has(key));
      for (const key of view.stepKeys) allOpen ? expanded.delete(key) : expanded.add(key);
      render();
    };
    const rows = document.createElement("div");
    rows.className = "chat-response-rows";
    content.append(summary, rows);
    const author = speaker(agentAuthor(session), true);
    row.append(author, content);
    view = { row, author, content, summary, rows, blocks: new Map(), stepKeys: [] };
    entryViews.set(viewKey, view);
  }
  usedEntryViews.add(viewKey);
  setText(view.author.lastElementChild, agentAuthor(session));
  const totals = responseSummary(entry.items);
  const active = isRunning(session) && entry.items.some((item) => item?.run_id && item.run_id === session.run?.run_id);
  const blocks = responseBlocks(entry.items);
  const singleIdenticalFold = isIdenticalSingleStepFold(entry.items, blocks);
  view.stepKeys = blocks.filter((block) => block.steps.length).map((block) => block.key);
  const open = active || (view.stepKeys.length > 0 && view.stepKeys.every((key) => expanded.has(key)));
  view.summary.hidden = singleIdenticalFold;
  setAttribute(view.summary, "aria-expanded", String(open));
  setText(view.summary, `${open ? "▾" : "▸"} Response · ${responseSummaryText(totals, entry.items.length)}`);
  view.row.classList.toggle("single-step-response", singleIdenticalFold);
  view.row.classList.toggle("alarm", totals.failed > 0);
  const usedBlocks = new Set(blocks.map((block) => block.key));
  reconcileChildren(view.rows, blocks.map((block) => renderResponseBlock(session, view, block, active)));
  for (const key of view.blocks.keys()) if (!usedBlocks.has(key)) view.blocks.delete(key);
  return view.row;
}

function renderResponseBlock(session, view, block, active) {
  let blockView = view.blocks.get(block.key);
  if (!blockView) {
    const root = document.createElement("div");
    root.className = "chat-response-block";
    blockView = { root, items: new Map(), prose: null, proseText: "", proseCaret: null, fold: null, head: null, rows: null };
    view.blocks.set(block.key, blockView);
  }
  const nodes = [];
  if (block.prose) nodes.push(renderResponseProse(blockView, block.prose));
  if (block.steps.length) nodes.push(renderResponseStepFold(session, blockView, block, active));
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
  view.prose.dataset.entryKey = item.key;
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

function renderResponseStepFold(session, view, block, active) {
  if (!view.fold) {
    view.fold = document.createElement("div");
    view.fold.className = "chat-step-fold";
    view.head = document.createElement("button");
    view.head.type = "button";
    view.head.className = "chat-step-summary";
    view.head.onclick = () => {
      expanded.has(block.key) ? expanded.delete(block.key) : expanded.add(block.key);
      render();
    };
    view.rows = document.createElement("div");
    view.rows.className = "chat-step-rows";
    view.fold.append(view.head, view.rows);
  }
  const totals = responseSummary(block.steps);
  // Item 2eo: one tool call and one thought are two rows, not a group.
  const headerless = !active && isHeaderlessSteps(block.steps);
  const open = active || headerless || expanded.has(block.key);
  view.head.hidden = headerless;
  view.fold.classList.toggle("headerless", headerless);
  view.fold.classList.toggle("alarm", totals.failed > 0);
  setAttribute(view.head, "aria-expanded", String(open));
  setText(view.head, `${open ? "▾" : "▸"} Steps · ${responseSummaryText(totals, block.steps.length)}`);
  const usedItems = new Set(block.steps.map((item, index) => item?.key || `invalid:${index}`));
  const nodes = [];
  if (open) {
    const rows = active ? block.steps : groupResponseRows(block.steps);
    for (const [index, item] of rows.entries()) {
      try {
        if (item?.kind === "tool-group") {
          usedItems.add(item.key);
          nodes.push(renderResponseToolGroup(session, view, item));
        } else {
          const key = item?.key || `invalid:${index}`;
          nodes.push(renderResponseItem(session, view, item, key));
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

function responseSummaryText(summary, rowCount) {
  const parts = [];
  if (summary.tools) parts.push(`${summary.tools} tool ${summary.tools === 1 ? "call" : "calls"}`);
  if (summary.failed) parts.push(`${summary.failed} failed`);
  if (summary.thoughts) parts.push(`${summary.thoughts} ${summary.thoughts === 1 ? "thought" : "thoughts"}`);
  if (!summary.tools && !summary.thoughts && summary.answers) parts.push(`${summary.answers} ${summary.answers === 1 ? "answer" : "answers"}`);
  if (!parts.length) parts.push(`${rowCount} ${rowCount === 1 ? "row" : "rows"}`);
  if (summary.duration) parts.push(formatDuration(summary.duration));
  return parts.join(" · ");
}

function renderResponseToolGroup(session, view, group) {
  let groupView = view.items.get(group.key);
  if (!groupView) {
    const root = document.createElement("div");
    root.className = "chat-tool-group";
    const head = document.createElement("button");
    head.type = "button";
    head.className = "chat-tool-group-head";
    head.onclick = () => {
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
  const parts = [`${group.tool} ×${group.calls}`];
  if (group.thoughts) parts.push(`+${group.thoughts} ${group.thoughts === 1 ? "thought" : "thoughts"}`);
  if (group.failed) parts.push(`${group.failed} failed`);
  if (group.duration) parts.push(formatDuration(group.duration));
  groupView.root.classList.toggle("alarm", group.failed > 0);
  setAttribute(groupView.head, "aria-expanded", String(open));
  setText(groupView.head, `${open ? "▾" : "▸"} ${parts.join(" · ")}`);
  const children = open ? group.items.map((item, index) => {
    try {
      if (item?.type === "agent" && item.reasoning) expanded.add(item.key);
      return renderResponseItem(session, view, item, item?.key || `invalid:group:${index}`, item?.type === "tool");
    }
    catch (error) {
      return renderFailure(item, error, index, true);
    }
  }) : [];
  reconcileChildren(groupView.children, children);
  return groupView.root;
}

function renderResponseItem(session, view, item, key, forceToolOpen = false) {
  if (!item || typeof item !== "object") throw new Error("entry is missing or is not an object");
  if (!item.key) throw new Error("entry key is missing");
  if (item.type === "notice") {
    const notice = noticeContent(session, item, false);
    notice.classList.add("chat-response-notice");
    notice.dataset.entryKey = item.key;
    return notice;
  }
  let itemView = view.items.get(key);
    if (!itemView) {
      const step = document.createElement("div");
      step.className = `chat-response-step ${item.type === "tool" ? "chat-response-tool" : ""}`;
      itemView = { step, answer: null, caret: null, answerText: "" };
      view.items.set(key, itemView);
    }
    itemView.step.dataset.entryKey = item.key;
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
    } else if (item.type === "tool") {
      stepNodes.push(toolTick(item, forceToolOpen));
    } else {
      throw new Error(`unsupported entry type ${String(item.type || "(missing)")}`);
    }
    reconcileChildren(itemView.step, stepNodes);
    return itemView.step;
}

function renderFailure(entry, error, index, nested) {
  const row = document.createElement(nested ? "div" : "section");
  row.className = nested ? "chat-response-step chat-response-notice chat-render-failure" : "chat-entry chat-notice-row chat-render-failure";
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
  });
  fileViews.set(key, { node, fingerprint });
  return node;
}

function speaker(name, agent = false) {
  const node = document.createElement("div");
  node.className = "chat-speaker";
  if (agent) {
    const image = document.createElement("img");
    image.src = "/static/assets/agent.svg";
    image.alt = "";
    node.append(image);
  }
  const label = document.createElement("span");
  label.textContent = name;
  node.append(label);
  return node;
}

function thinking(entry, tokens) {
  return thinkingRenderer.render(entry, tokens);
}

function toolTick(entry, forceOpen = false) {
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
    collapse.onclick = () => { expanded.delete(entry.key); render(); };
    button.onclick = () => {
      expanded.has(entry.key) ? expanded.delete(entry.key) : expanded.add(entry.key);
      render();
    };
    root.append(button);
    view = { root, button, pre, collapse, args: null, result: null, content: null };
    toolViews.set(entry.key, view);
  }
  // Item 2eo: a harness note is a line of its own, never part of the result.
  const note = typeof entry.result?.harness_note === "string" ? entry.result.harness_note : "";
  if (note) {
    if (!view.note) { view.note = document.createElement("div"); view.note.className = "tool-harness-note"; }
    setText(view.note, `harness · ${note}`);
    if (view.root.firstChild !== view.note) view.root.prepend(view.note);
  } else if (view.note?.isConnected) {
    view.note.remove();
  }
  const open = forceOpen || expanded.has(entry.key);
  setAttribute(view.button, "aria-expanded", String(open));
  const state = entry.result && typeof entry.result.ok === "boolean" ? (entry.result.ok ? "ok" : "error") : "";
  setText(view.button.children[0], `${open ? "▾" : "▸"} ${entry.name}`);
  setText(view.button.children[1], keyArgument(entry.args));
  setText(view.button.children[2], callServiceStatus(entry.name, entry.result) || state);
  setAttribute(view.button.children[2], "class", `tool-state ${state === "error" ? "error" : ""}`);
  setText(view.button.children[3], formatDuration(entry.result?.ms));
  if (open) {
    if (!view.pre.textContent || view.args !== entry.args || view.result !== entry.result || view.content !== entry.content) {
      view.pre.textContent = `arguments\n${JSON.stringify(entry.args, null, 2)}\n\nresult\n${capResult(entry.content)}`;
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

function formatThoughtSeconds(milliseconds) {
  if (!Number.isFinite(milliseconds)) return "";
  const seconds = Math.max(0, milliseconds) / 1000;
  return (seconds < 10 ? seconds.toFixed(1) : seconds.toFixed(0)).replace(/\.0$/, "");
}

function renderNotice(session, entry) {
  const row = document.createElement("div");
  row.className = "chat-entry chat-notice-row";
	const content = noticeContent(session, entry, false);
  if (content.classList.contains("alarm")) row.classList.add("alarm");
  row.append(content);
  return row;
}

function noticeContent(session, entry, actionable) {
  const event = entry.event;
  if (!event || typeof event !== "object") throw new Error("notice event is missing or is not an object");
  const data = event.data && typeof event.data === "object" ? event.data : {};
  const content = document.createElement("div");
  content.className = "chat-content";
  if (event.type === "run.stopped") {
    const reason = (data.reason || "").replaceAll("_", " ");
		if (data.reason === "model_unreachable") {
			const line=document.createElement("span"); line.textContent=`model unreachable · ${entry.text || session.model_unreachable?.host || "model"}`; if(data.detail) line.title=data.detail; content.append(line);
		} else if (data.reason === "aborted_mid_tool") {
			content.textContent = `stopped mid-tool${data.queue_held ? " · queued messages held" : ""}`;
			if (data.detail) content.title = data.detail;
		} else content.textContent = `stopped: ${reason}${data.detail ? ` · ${data.detail}` : ""}`;
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
      content.textContent = `delivery: ${parts.join(" · ")}`;
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
  else if (event.type === "compaction") content.textContent = `compacted ${signed((data.after || 0) - (data.before || 0))} tokens${data.profile_id ? ` via ${data.profile_id}` : ""}`;
  else if (event.type === "workspace.conflict") {
    content.textContent = `conflict: ${data.path} written by ${data.other_label} ${data.age_s} s ago`;
    content.classList.add("alarm");
  } else if (event.type === "memory.noted") content.textContent = "noted for next session";
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
	else if (event.type === "file.grant") content.textContent = `file-tool grant: operator · for this ${data.scope === "session" ? "chat" : "run"}`;
	else if (event.type === "file.grant_lapsed") content.textContent = `file-tool grant lapsed: ${data.scope === "session" ? "chat closed" : "run ended"}`;
	else if (event.type === "approval.required") {
		return createApprovalCard(document, entry, {
			replay: store.replay,
			decide: actionable ? (callID, decision) => api("/api/approve", { session_id: session.id, call_id: callID, decision }) : null,
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

// Item 2ge: the microphone. Trace while it listens, Mute at rest, and the
// hover text says whether the recogniser is running offline on this host or
// through the online path. The operator asked for the Windows recogniser -
// the one behind Win+H - so there is no browser speech API here and no audio
// leaves the machine by any path Windows itself does not take.
let micState = { listening: false, available: null, offline: null, reason: "" };

function renderMic(session) {
  if (!mic) return;
  const usable = !!session && !store.replay && micState.available !== false;
  mic.disabled = !usable;
  mic.dataset.state = micState.listening ? "listening" : "idle";
  mic.classList.toggle("listening", micState.listening);
  // Item 2ge: the hover says which engine is listening and whether anything
  // would leave the machine if it did. The host answers that; this draws the
  // answer rather than deciding it.
  const where = micState.available === false
    ? (micState.reason || "dictation is unavailable on this host")
    : (micState.reason || (micState.offline === false ? "online" : "offline"));
  const label = micState.listening ? "Stop dictating" : "Dictate";
  mic.setAttribute("aria-label", label);
  mic.setAttribute("title", label + " \u00b7 " + where);
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
  send.disabled = !session || !!store.replay;
  input.disabled = !session || !!store.replay;
  attachButton.disabled = !session || !!store.replay || attachmentsBusy;
  input.removeAttribute("placeholder");
  const queued = session?.queued_messages || 0;
  // Item 2fc: a run queued behind its model profile says whose run it waits on.
  const queuedBehind = session?.run?.status === "queued" && session.run.waiting_behind ? `waiting for model · behind ${session.run.waiting_behind}` : "";
  const state = session?.pending_approval || session?.pending_repo_policy ? "waiting for you" : queuedBehind || session?.run?.status || "idle";
  const unreachable = session?.model_unreachable;
  const busy = session?.model_busy;
  const operatorUntil = store.shell_identity?.operator_context ? `operator mode · until ${shortTime(store.shell_identity.operator_context_expires_at)}` : "";
  const queueText = queued ? `queued (${queued})${unreachable ? " · waiting for model" : ""}` : "";
  const activity = liveActivityText(session);
  // Item 2eo: "remove the redundant text above chat that says model busy -
  // model unavailable. just put 'model busy'". A busy or unreachable model is
  // the whole line, and the two never appear together; the host is on hover.
  const modelLine = unreachable ? "model unreachable" : busy ? "model busy" : "";
  const primary = session && !session.runnable ? session.not_runnable_reason : modelLine || activity || state;
  const message = localNotice || (modelLine && session?.runnable !== false ? modelLine : [primary, queueText, operatorUntil].filter(Boolean).join(" · "));
  // Live state, not decoration: the robot runs beside the live line for exactly
  // as long as the run is live, and is absent otherwise. Its eyes take the same
  // state colour the tab robot uses.
  const running = !!activity;
  notice.replaceChildren();
  if (running) {
    const glyph = document.createElement("span");
    glyph.className = `chat-run-robot ${session?.model_unreachable ? "offline" : session?.pending_approval || session?.pending_repo_policy ? "waiting" : "running"}`;
    glyph.setAttribute("aria-hidden", "true");
    glyph.innerHTML = '<img src="/static/assets/agent.svg" alt=""><span class="chat-run-robot-eyes"></span>';
    notice.append(glyph);
  }
  const text = document.createElement("span");
  text.className = "chat-notice-text";
  text.textContent = message;
  if (modelLine && message === modelLine) text.title = (unreachable || busy)?.host || "";
  notice.append(text);
  notice.className = `chat-notice ${localAlarm || unreachable || (session && !session.runnable) ? "alarm" : ""}`;
	pendingFiles.replaceChildren(...queuedAttachments.map((file) => {
    const row = document.createElement("span");
    row.className = "chat-pending-file";
    const label = document.createElement("span");
    const sidecar = file.sidecar ? ` · ${file.tier === "ocr" ? "OCR" : "extracted"}: ${file.sidecar.split("/").pop()}` : "";
    label.textContent = `${file.path.split("/").pop()} · ${format(file.bytes)} B${file.reused ? " · reused" : ""}${sidecar}`;
    row.append(label);
    const warning = attachmentReadability(session, store.servers, file);
    if (warning) {
      const reason = document.createElement("span");
      reason.className = "chat-attachment-warning";
      reason.textContent = warning;
      row.append(reason);
    }
    return row;
	}));
	const worker = workerApproval(store.sessions, session);
	pendingApproval.hidden = !(session?.pending_approval || session?.pending_repo_policy || worker);
	const policyCard = session?.pending_repo_policy ? createPolicyCard(session) : null;
	const workerCard = worker ? [createApprovalCard(document, worker.pending_approval, {
		replay: store.replay,
		author: agentAuthor(worker, "c"),
		decide: (callID, decision) => api("/api/approve", { session_id: worker.id, call_id: callID, decision }),
	})] : [];
	pendingApproval.replaceChildren(...(session?.pending_approval ? [createApprovalCard(document, session.pending_approval, {
		replay: store.replay,
		decide: (callID, decision) => api("/api/approve", { session_id: session.id, call_id: callID, decision }),
	})] : policyCard ? [policyCard] : []), ...workerCard);
  renderSendStop(send, session, store.replay);
  renderMic(session);
  retryModel.hidden = !unreachable;
  retryModel.disabled = !session || store.replay;
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
  const session = store.sessions[selectedID()];
  if (!session || store.replay) return;
  const text = input.value.trim();
  if (!text && !queuedAttachments.length) return;
  try {
    // Item 2gn: a closed chat opened by name is read-only until the operator
    // sends. Sending reopens it first, through the route the history list
    // already uses, so the message lands in a chat that is open.
    if (session.closed) await api(`/api/sessions/${encodeURIComponent(session.id)}/reopen`, {});
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
      const uploaded = await uploadAttachment(file, session.id, { token: store.mutation_token });
      if (!queuedAttachments.some((item) => item.path.toLowerCase() === uploaded.path.toLowerCase())) queuedAttachments.push(uploaded);
      localNotice = uploaded.note || "Attachment ready";
    }
  } catch (error) {
    localNotice = error.message || String(error);
    localAlarm = true;
  } finally {
    attachmentsBusy = false;
    renderComposer(session);
  }
}

async function refreshExchangeFiles() {
  if (store.replay || !store.sessions[selectedID()]) return;
  try {
    const files = await exchangeFiles();
    exchangeFileList.replaceChildren(...files.map((file) => {
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = `${file.path} · ${format(file.bytes)} B`;
      button.onclick = () => void queueExchangeFile(file);
      return button;
    }));
    if (!files.length) {
      const empty = document.createElement("span");
      empty.textContent = "Attachments folder is empty";
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
  attachmentsBusy = true;
  localNotice = "Copying from attachments…";
  localAlarm = false;
  renderComposer(session);
  try {
    const uploaded = await exchangeUpload(item, session.id, { token: store.mutation_token, maxBytes: store.config.tools?.attachments?.max_bytes });
    if (!queuedAttachments.some((value) => value.path.toLowerCase() === uploaded.path.toLowerCase())) queuedAttachments.push(uploaded);
    localNotice = uploaded.note || "Attachment ready";
    attachMenu.hidden = true;
  } catch (error) {
    localNotice = error.message || String(error);
    localAlarm = true;
  } finally {
    attachmentsBusy = false;
    renderComposer(session);
  }
}

// Item 2ge: click to dictate, click again to stop. Recognised text lands in
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
      stopDictation();
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
    stopDictation();
  };
}

if (mic) {
  mic.onclick = () => {
    if (micState.listening) stopDictation();
    else startDictation();
  };
}

// Item 2gk: labelling the last run moved here with the readout; it is the same
// call the dissolved page made.
readoutLabel.addEventListener("click", (event) => {
  const button = event.target.closest("button[data-label]");
  const session = store.sessions[selectedID()];
  const runID = session?.run?.last_run_id;
  if (!button || !session || !runID || store.replay) return;
  void api(`/api/sessions/${encodeURIComponent(session.id)}/result-label`, { label: button.dataset.label, run_id: runID }).catch((error) => { notice.textContent = error.message; notice.classList.add("alarm"); });
});

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
  try { await api(`/api/servers/${encodeURIComponent(session.server_id || session.b_profile)}/probe`, { session_id: session.id, retry: true }); }
  catch (error) { localNotice = error.message; localAlarm = true; renderComposer(session); }
};
attachButton.onclick = () => { attachMenu.hidden = !attachMenu.hidden; };
attachBrowse.onclick = () => { attachMenu.hidden = true; filePicker.click(); };
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
expandComposer.onclick = () => {
  composerExpanded = !composerExpanded;
  resize();
  input.focus();
};
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
log.addEventListener("scroll", () => {
  follow = log.scrollHeight - log.clientHeight - log.scrollTop <= 24;
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

function resize() {
  composer.classList.toggle("expanded", composerExpanded);
  expandComposer.textContent = composerExpanded ? "↧" : "↥";
  expandComposer.setAttribute("aria-label", composerExpanded ? "Collapse composer" : "Expand composer");
}
function busy(session) {
  return !!session && ["running", "queued", "paused", "stopping"].includes(session.run?.status);
}
function keyArgument(args) {
  if (!args || typeof args !== "object" || Array.isArray(args)) return "";
  const service = callServiceKey(args);
  if (service) return service;
  for (const key of ["path", "command", "pattern", "note"]) if (args[key] !== undefined) return String(args[key]);
  const first = Object.values(args)[0];
  return first === undefined ? "" : typeof first === "string" ? first : JSON.stringify(first);
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
function shortTime(value) {
  const date = new Date(value || "");
  return Number.isNaN(date.getTime()) ? "soon" : date.toLocaleTimeString([], {hour:"2-digit", minute:"2-digit"});
}
