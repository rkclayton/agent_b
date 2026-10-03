export function operatorLogEntry(data = {}) {
  const expires = data.expires_at
    ? ` · until ${new Date(data.expires_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
    : "";
  if (!data.enabled)
    return { text: `Run as you disabled${data.reason && !/^disabled by (user|operator) request$/.test(data.reason) ? ` · ${data.reason}` : ""}`, alarm: false };
  if (String(data.reason || "").startsWith("idle window reset:"))
    return { text: `Run as you active · idle deadline extended${expires}`, alarm: true };
  return { text: `Run as you enabled${expires}`, alarm: true };
}
