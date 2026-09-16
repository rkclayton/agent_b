export function callServiceKey(args) {
  if (!args?.service || !args?.method) return "";
  return `${args.service} ${String(args.method).toUpperCase()} ${args.path || ""}`.trim();
}

export function callServiceStatus(name, result) {
  if (name !== "call_service" || result?.status === undefined || result?.status === null) return "";
  return `HTTP ${result.status}`;
}

export function callServiceFlowReadout(session, formatDuration) {
  if (session?.activity?.stage !== "execute") return "";
  const calls = [...(session.chat || [])].reverse();
  const item = calls.find((entry) => entry.type === "tool" && entry.name === "call_service");
  if (!item || (session.activity.active_tool && session.activity.active_tool !== "call_service")) return "";
  const result = item.result || {};
  const status = callServiceStatus(item.name, result) || "running";
  const duration = result.ms === undefined || result.ms === null ? "" : ` · ${formatDuration(result.ms)}`;
  return `${callServiceKey(item.args)} · ${status}${duration}`;
}
