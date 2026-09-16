export function operatorLogEntry(data = {}) {
  const expires = data.expires_at
    ? ` · until ${new Date(data.expires_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
    : "";
  if (!data.enabled)
    return { text: `Operator mode disabled${data.reason && data.reason !== "disabled by operator request" ? ` · ${data.reason}` : ""}`, alarm: false };
  if (String(data.reason || "").startsWith("idle window reset:"))
    return { text: `Operator mode active · idle deadline extended${expires}`, alarm: true };
  return { text: `Operator mode enabled${expires}`, alarm: true };
}
