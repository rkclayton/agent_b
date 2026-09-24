export async function acquireBrowserSessionToken() {
	const metaToken = globalThis.document?.querySelector?.('meta[name="agentb-mutation-token"]')?.content || "";
	if (!globalThis.location) return metaToken;
	const fragment = new URLSearchParams(location.hash.slice(1));
  const bootstrapToken = fragment.get("agentb-bootstrap") || "";
  if (!bootstrapToken) return metaToken;

  history.replaceState(null, "", `${location.pathname}${location.search}`);
  const response = await fetch("/api/browser-session", {
    method: "POST",
    headers: { "X-AgentB-Mutation-Token": bootstrapToken },
  });
  if (!response.ok) throw new Error(`browser session bootstrap failed: HTTP ${response.status}`);
  return bootstrapToken;
}
