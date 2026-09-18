// Item 2ev: a page from one build never keeps running against another build's
// server. The server stamps each document with the build id that served it. The
// shell compares it with the running server's build on load and on every state
// snapshot (so a window left open across an upgrade notices when its event
// stream reconnects to the new server) and reloads, once per server build, when
// they differ. It also removes any service worker an earlier build could have
// registered (none ever did; the step is harmless).

const reloadKey = "agentb.build-reload";

export function decideBuildAction(pageBuild, serverBuild, reloadedFor) {
  if (!serverBuild) return "unknown";
  if (pageBuild === serverBuild) return "current";
  if (reloadedFor === serverBuild) return "stale";
  return "reload";
}

function defaultStorage() {
  try { return sessionStorage; } catch { return null; }
}

function readReloaded(storage) {
  try { return storage?.getItem(reloadKey) || ""; } catch { return ""; }
}

// Returns whether the marker is really stored: without it a reload could loop.
function writeReloaded(storage, value) {
  try {
    if (value) storage.setItem(reloadKey, value);
    else storage.removeItem(reloadKey);
    return readReloaded(storage) === value;
  } catch {
    return false;
  }
}

export function pageBuild(doc = document) {
  return doc?.querySelector?.('meta[name="agentb-build"]')?.content || "";
}

export function compareWithServer(serverBuild, { doc = document, storage = defaultStorage(), reload = () => location.reload() } = {}) {
  const action = decideBuildAction(pageBuild(doc), serverBuild, readReloaded(storage));
  if (action === "current") writeReloaded(storage, "");
  if (action === "reload") {
    if (!writeReloaded(storage, serverBuild)) return "stale";
    reload();
  }
  return action;
}

export async function checkBuild({ doc = document, nav = navigator, storage = defaultStorage(), fetcher = fetch, reload = () => location.reload() } = {}) {
  if (nav.serviceWorker?.getRegistrations) {
    try {
      for (const registration of await nav.serviceWorker.getRegistrations()) await registration.unregister();
    } catch {}
  }
  let serverBuild = "";
  try {
    const response = await fetcher("/api/state", { cache: "no-store" });
    if (response.ok) serverBuild = (await response.json())?.build?.executable_sha256 || "";
  } catch {}
  return compareWithServer(serverBuild, { doc, storage, reload });
}

if (typeof document !== "undefined" && typeof window !== "undefined") {
  checkBuild().catch(() => {});
}
