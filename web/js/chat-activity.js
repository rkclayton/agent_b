export function liveActivityText(session) {
  if (session?.run?.status !== "running") return "";
  const activity = session.activity || {};
  if (activity.stage_state === "exit") {
    switch (activity.stage) {
      case "assemble": return "waiting for model";
      case "call_model": return "waiting for response parsing";
      case "parse": return "waiting for next action";
      case "dispatch": return "waiting for tool execution";
      case "execute": return "waiting for result recording";
      case "append": return "waiting for context check";
      case "compact": return "waiting for next turn";
      case "wait_user": return "waiting to resume";
      default: return "waiting · state unknown";
    }
  }
  if (activity.stage_state !== "enter") return "waiting · state unknown";
  switch (activity.stage) {
    case "assemble": return "assembling turn";
    case "call_model": return modelRequestText(activity);
    case "parse": return "parsing model response";
    case "dispatch": return "preparing tool call";
    case "execute": return activity.active_tool ? `tool executing · ${activity.active_tool}` : "tool executing · unknown";
    case "append": return "recording tool result";
    case "compact": return "compacting context";
    case "wait_user": return "waiting for you";
    default: return "waiting · state unknown";
  }
}

export function modelRequestText(activity = {}) {
  const stream = activity.stream || {};
  const calls = Array.isArray(stream.tool_calls) ? stream.tool_calls : [];
  const call = calls.at(-1);
  if (call) return `calling ${call.name || "tool"} · ${formatBytes(call.argument_bytes)} · ${formatElapsed(call.started_at, call.last_chunk_at)}`;
  const reasoning = Number(stream.reasoning_chars || 0);
  const written = Math.max(0, Number(stream.total_chars || 0) - reasoning);
  if (written > 0) return `writing · ${estimatedTokens(written)} tokens`;
  if (reasoning > 0) return `thinking · ${estimatedTokens(reasoning)} tokens`;
  const processed = Number(activity.progress?.processed || 0);
  return `prompt ${processed.toLocaleString("en-US")} tokens processing`;
}

function estimatedTokens(characters) { return Math.max(0, Math.ceil(characters / 3.6)); }
function formatBytes(value) {
  const bytes = Math.max(0, Number(value || 0));
  return bytes < 1000 ? `${bytes} B` : `${(bytes / 1000).toFixed(bytes < 10000 ? 1 : 0)} kB`;
}
function formatElapsed(start, end) {
  const seconds = Math.max(0, Math.round((Number(end || start || 0) - Number(start || 0)) / 1000));
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m${String(seconds % 60).padStart(2, "0")}s`;
}

export function showsStreamCaret(entry) {
  return entry?.type === "agent" && !entry.done && !!entry.text;
}
