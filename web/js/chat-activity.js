export function liveActivityText(session, now = Date.now()) {
  const status = session?.run?.status;
  const activity = session?.activity || {};
  const elapsed = formatElapsed(activity.state_started_at || activity.tool_started_at || activity.started_at, now);
  const withElapsed = (text) => `${text} · ${elapsed}`;
  if (session?.pending_approval || session?.pending_repo_policy) {
    const event = session.pending_approval?.event || session.pending_repo_policy?.event || {};
    return withElapsed(`waiting for you — ${event.data?.name || event.type || "decision"}`);
  }
  if (!new Set(["running", "queued", "paused", "stopping"]).has(status)) return "";
  if (status === "queued") return withElapsed(session.run.waiting_behind ? `queued — behind ${session.run.waiting_behind}` : "queued — waiting for a model slot");
  if (status === "stopping") return withElapsed("stopping the run");
  if (activity.active_tool === "delegate" && activity.delegate?.status === "running") {
    const child = activity.delegate;
    const step = child.tool ? `running ${child.tool}${child.target ? ` ${child.target}` : ""}` : child.stage === "call_model" ? "thinking" : child.stage || "starting";
    return withElapsed(`delegate — ${step}${child.turn ? ` · turn ${child.turn}` : ""}`);
  }
  if (activity.active_tool) {
    const target = activity.tool_target ? ` ${activity.tool_target}` : "";
    const line = activity.tool_last_line ? ` · ${activity.tool_last_line}` : "";
    return withElapsed(`running ${activity.active_tool}${target}${line}`);
  }
  if (activity.stage_state === "exit") {
    switch (activity.stage) {
      case "assemble": return withElapsed("waiting for model");
      case "call_model": return withElapsed("waiting for response parsing");
      case "parse": return withElapsed("waiting for next action");
      case "dispatch": return withElapsed("waiting for tool execution");
      case "execute": return withElapsed("waiting for result recording");
      case "append": return withElapsed("recording result");
      case "compact": return withElapsed("waiting for next turn");
      case "wait_user": return withElapsed("waiting to resume");
      default: return withElapsed("waiting — state unknown");
    }
  }
  if (activity.stage_state !== "enter") return withElapsed("waiting — state unknown");
  switch (activity.stage) {
    case "assemble": return withElapsed("assembling turn");
    case "call_model": return withElapsed(modelRequestText(activity, session?.model_busy));
    case "parse": return withElapsed("parsing model response");
    case "dispatch": return withElapsed("preparing tool call");
    case "execute": return withElapsed("running tool — target unknown");
    case "append": return withElapsed("recording tool result");
    case "compact": return withElapsed(activity.compaction_reason === "server_size_limit" ? "trimming for server size limit" : "compacting — fitting context to the model window");
    case "wait_user": return withElapsed("waiting for you");
    default: return withElapsed("waiting — state unknown");
  }
}

export function modelRequestText(activity = {}, busy = null) {
  const stream = activity.stream || {};
  const calls = Array.isArray(stream.tool_calls) ? stream.tool_calls : [];
  const call = calls.at(-1);
  if (call) return `calling ${call.name || "tool"} · ${formatBytes(call.argument_bytes)} · ${formatElapsed(call.started_at, call.last_chunk_at)}`;
  const reasoning = Number(stream.reasoning_chars || 0);
  const written = Math.max(0, Number(stream.total_chars || 0) - reasoning);
  if (written > 0) return `writing · ${estimatedTokens(written)} tokens`;
  if (reasoning > 0) return `thinking · ${estimatedTokens(reasoning)} tokens`;
  const processed = Number(activity.progress?.processed || 0);
  const total = Number(activity.progress?.total || 0);
  const cached = Number(activity.progress?.cache || 0);
  if (processed > 0) return `reading ${formatK(processed)}${total > 0 ? ` of ${formatK(total)}` : ""}${cached > 0 ? `, ${formatK(cached)} cached` : ""}`;
	if (busy?.detail === "loading model") return "loading model";
	return busy ? "waiting for model" : "connecting";
}

function formatK(value) { return `${Math.max(0.1, Number(value) / 1000).toFixed(1).replace(/\.0$/, "")}k`; }

function estimatedTokens(characters) { return Math.max(0, Math.ceil(characters / 3.6)); }
function formatBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  return bytes < 1000 ? `${bytes} B` : `${(bytes / 1000).toFixed(bytes < 10000 ? 1 : 0)} kB`;
}
function formatElapsed(start, end) {
  const seconds = Math.max(0, Math.floor((Number(end || start || 0) - Number(start || end || 0)) / 1000));
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m${String(seconds % 60).padStart(2, "0")}s`;
}

export function showsStreamCaret(entry) {
  return entry?.type === "agent" && !entry.done && !!entry.text;
}
