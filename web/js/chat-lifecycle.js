const activeStates = new Set(["running", "queued", "paused", "stopping"]);

export function openSessions(sessions = {}) {
  return Object.values(sessions).filter((session) => !session.closed);
}

function firstUserLine(session) {
  const text = (session?.chat || []).find((entry) => entry.type === "user")?.text || "New chat";
  return text.split(/\r?\n/, 1)[0].trim() || "New chat";
}

export function sessionTitle(session) {
  if (!session) return "";
  return session.agent_name || session.agent_id || "agent";
}

export function agentAuthor() { return "agent_b"; }

// a chat shows its NAME. Before the operator has said
// anything there is nothing to name it after, so it reads "new chat" - never
// the role, which the robot glyph and its hover already carry.
export function chatName(session) {
  const label = session?.label == null ? "" : String(session.label).trim();
  if (label && label.toLowerCase() !== "null") return label;
  return firstUserLine(session);
}

export function chatRowText(session) {
  return chatName(session);
}

export function appRobotStatus(sessions = {}, selectedID = "") {
  const chats = Object.values(sessions).filter((session) => !session.closed);
  const waiting = chats.filter((session) => session.pending_approval || session.pending_repo_policy || session.run?.status === "paused").length;
  const running = chats.filter((session) => ["running", "queued", "stopping"].includes(session.run?.status)).length;
  const offline = !!sessions[selectedID]?.model_unreachable;
  const detail = [waiting && `${waiting} waiting for you`, running && `${running} running`, offline && "model unreachable"].filter(Boolean);
  return { state: waiting ? "waiting" : running ? "running" : offline ? "offline" : "idle", label: detail.join(" · ") || "idle" };
}

export function isRunning(session) {
  return activeStates.has(session?.run?.status);
}
