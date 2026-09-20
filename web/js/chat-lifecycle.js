const activeStates = new Set(["running", "queued", "paused", "stopping", "held"]);

export function openSessions(sessions = {}) {
  return Object.values(sessions).filter((session) => !session.closed);
}

export function firstUserLine(session) {
  const text = (session?.chat || []).find((entry) => entry.type === "user")?.text || "New chat";
  return text.split(/\r?\n/, 1)[0].trim() || "New chat";
}

export function sessionTitle(session) {
  if (!session) return "";
  // Item 2eo: "lets leave just the active model name" — the tab already carries the role.
  return session.b_profile || session.server_id || "profile";
}

export function agentAuthor(session, role = "b") {
  const letter = ["b", "c", "d"].includes(role) ? role : role === "aux" ? "c" : "b";
  return `agent_${letter}`;
}

// workerApproval is the card the worker of a plan is waiting on. The worker has
// no chat, so its approvals and cycle decisions are drawn in the design thread
// of the plan it is bound to, and answered against the session of the worker.
export function workerApproval(sessions, session) {
  const planID = session?.plan_id;
  if (!planID || session?.role === "c") return null;
  return Object.values(sessions || {}).find((item) => item?.role === "c" && !item.closed && item.plan_id === planID && item.pending_approval) || null;
}

export function sameWorkerPlan(sessions, sessionID, selectedID) {
  const worker = sessions?.[sessionID];
  const selected = sessions?.[selectedID];
  return !!worker && worker.role === "c" && !!worker.plan_id && worker.plan_id === selected?.plan_id;
}

export function runCount(session) {
  const ids = new Set();
  for (const event of session?.timeline || []) {
    if (event.type === "run.started") ids.add(event.run_id || event.data?.run_id);
  }
  ids.delete("");
  ids.delete(undefined);
  return ids.size;
}

export function relativeTime(value, now = Date.now()) {
  const at = Date.parse(value || "");
  if (!Number.isFinite(at)) return "now";
  const seconds = Math.max(0, Math.floor((now - at) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

export function stateGlyph(session) {
  const state = session?.run?.status || "idle";
  if (state === "running") return "●";
  if (state === "queued") return "◌";
  if (state === "paused") return "Ⅱ";
  if (state === "stopping") return "■";
  if (state === "held") return "◇";
  return "○";
}

// Item 2go (v1.2.5): a chat shows its NAME. Before the operator has said
// anything there is nothing to name it after, so it reads "new chat" - never
// the role, which the robot glyph and its hover already carry.
export function chatName(session) {
  const label = String(session?.label || "").trim();
  return label || "new chat";
}

// Item 2go, the operator on the history list: "i want it to display like this:
// MM:DD · Chat name · ×, nothing more." The row is the date it was created and
// the name; the × is a control beside it, not text.
export function chatRowText(session) {
  return `${chatRowDate(session)} · ${chatName(session)}`;
}

export function chatRowDate(session, now = new Date()) {
  const at = session?.created_at ? new Date(session.created_at) : null;
  const when = at && !Number.isNaN(at.getTime()) ? at : now;
  return `${String(when.getMonth() + 1).padStart(2, "0")}:${String(when.getDate()).padStart(2, "0")}`;
}

export function isRunning(session) {
  return activeStates.has(session?.run?.status);
}
