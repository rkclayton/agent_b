// Proposed plan edits, rendered in the chat that proposed them (item 2fc).
import { api, store } from "./bus.js";
import { proposalKey, proposalLabel, workerProposals } from "./plan-surface.js";

// Item 2fc: the planner IS the d-chat, so its proposed edits sit in its own
// thread, above the composer, with the tray's controls: Accept, Dismiss, and a
// click that quotes the proposal into the composer. The Plan page has no tray.
const resolvedKey = (id) => `agentb.plan.resolved.${id}`;
let drawn = "";

function readSet(key) {
  try { return new Set(JSON.parse(localStorage.getItem(key) || "[]")); } catch { return new Set(); }
}

function remember(key, value) {
  const values = readSet(key);
  values.add(value);
  try { localStorage.setItem(key, JSON.stringify([...values])); } catch { /* the row still goes; it may return after a reload */ }
}

export function pendingProposals(session, sessions = store.sessions) {
  if (!session) return [];
  const resolved = readSet(resolvedKey(session.id));
  return [...(session.messages || []).flatMap((message) => message.plan_proposals || []), ...workerProposals(sessions, session)]
    .filter((proposal) => !resolved.has(proposalKey(proposal)));
}

export function renderChatProposals(root, session, input) {
  if (!root) return;
  const proposals = pendingProposals(session);
  const key = `${session?.id || ""}|${proposals.map(proposalKey).join("|")}`;
  if (key === drawn) return;
  drawn = key;
  root.hidden = proposals.length === 0;
  root.replaceChildren(...proposals.map((proposal) => {
    const row = document.createElement("article");
    row.className = "plan-proposal";
    row.tabIndex = 0;
    row.title = "Click to quote this proposal into your message";
    const label = document.createElement("span");
    label.textContent = proposalLabel(proposal);
    const text = document.createElement("code");
    text.textContent = proposal.old_text;
    const actions = document.createElement("div");
    actions.className = "plan-proposal-actions";
    const accept = button("Accept");
    const dismiss = button("Dismiss");
    // A verifier request has no text to accept: only the planner can name the command.
    if (proposal.kind !== "verifier") actions.append(accept);
    actions.append(dismiss);
    const error = document.createElement("p");
    error.className = "field-error";
    error.hidden = true;
    row.append(label, text, actions, error);
    const quote = () => {
      input.value = `${input.value}${input.value ? "\n" : ""}> ${proposalLabel(proposal)}\n> ${proposal.new_text || proposal.old_text}`;
      input.focus();
    };
    row.onclick = (event) => { if (!event.target.closest("button")) quote(); };
    row.onkeydown = (event) => { if (event.key === "Enter" && !event.target.closest("button")) quote(); };
    accept.onclick = async () => {
      accept.disabled = true;
      try {
        await api("/api/plan/accept", { session_id: session.id, proposal });
        remember(resolvedKey(session.id), proposalKey(proposal));
        drawn = "";
        renderChatProposals(root, store.sessions[session.id], input);
      } catch (failure) {
        error.textContent = failure.message;
        error.hidden = false;
        accept.disabled = false;
      }
    };
    dismiss.onclick = () => {
      remember(resolvedKey(session.id), proposalKey(proposal));
      drawn = "";
      renderChatProposals(root, store.sessions[session.id], input);
    };
    return row;
  }));
}

function button(text) {
  const value = document.createElement("button");
  value.type = "button";
  value.textContent = text;
  return value;
}
