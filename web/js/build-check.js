// Item 2ev: a page from one build never runs against another build's server.
// The server stamps each document with the build id that served it; on load the
// shell compares it with the running server's build and reloads, once per
// server build, when they differ. It also removes any service worker an earlier
// build could have registered (none ever did; the step is harmless).

const reloadKey = "agentb.build-reload";

export function decideBuildAction(pageBuild, serverBuild, reloadedFor) {
  if (!serverBuild) return "unknown";
  if (pageBuild === serverBuild) return "current";
  if (reloadedFor === serverBuild) return "stale";
  return "reload";
}

function readReloaded(storage) {
  try { return storage.getItem(reloadKey) || ""; } catch { return ""; }
}

function writeReloaded(storage, value) {
  try {
    if (value) storage.setItem(reloadKey, value);
    else storage.removeItem(reloadKey);
  } catch {}
}

export async function checkBuild({ doc = document, nav = navigator, storage = sessionStorage, fetcher = fetch, reload = () => location.reload() } = {}) {
  if (nav.serviceWorker?.getRegistrations) {
    try {
      for (const registration of await nav.serviceWorker.getRegistrations()) await registration.unregister();
    } catch {}
  }
  const pageBuild = doc.querySelector('meta[name="agentb-build"]')?.content || "";
  let serverBuild = "";
  try {
    const response = await fetcher("/api/state", { cache: "no-store" });
    if (response.ok) serverBuild = (await response.json())?.build?.executable_sha256 || "";
  } catch {}
  const action = decideBuildAction(pageBuild, serverBuild, readReloaded(storage));
  if (action === "current") writeReloaded(storage, "");
  if (action === "reload") {
    writeReloaded(storage, serverBuild);
    reload();
  }
  return action;
}

if (typeof document !== "undefined" && typeof window !== "undefined") {
  checkBuild();
}
