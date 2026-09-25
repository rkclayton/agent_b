async function credential(value) {
  const cache = await caches.open("agentb-phone");
  if (value === undefined) return (await cache.match("/phone-credential"))?.headers.get("Authorization")?.slice(7) || "";
  if (!value) return cache.delete("/phone-credential");
  return cache.put("/phone-credential", new Response("", { headers: { Authorization: `Bearer ${value}` } }));
}
self.addEventListener("install", () => self.skipWaiting()); self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));
self.addEventListener("message", (event) => { if (event.data?.action === "credential.set") event.waitUntil(credential(event.data.value || "").then(() => event.ports[0]?.postMessage("ok"))); });
self.addEventListener("fetch", (event) => {
  const target = new URL(event.request.url); if (target.origin !== self.location.origin || !target.pathname.startsWith("/api/")) return;
  event.respondWith(Promise.all([credential(), self.clients.get(event.clientId)]).then(([token, client]) => {
    if (!client || !new URL(client.url).pathname.startsWith("/phone") || !token) return fetch(event.request);
    const headers = new Headers(event.request.headers); headers.set("Authorization", `Bearer ${token}`);
    return fetch(new Request(event.request, { headers }));
  }));
});
self.addEventListener("push", (event) => { const notice = event.data?.json() || {};
  event.waitUntil(self.registration.showNotification(notice.title || "Agent_b", { body: notice.body || "Agent_b needs you.", data: { url: notice.url || "/phone" }, tag: notice.chat_id || "agentb" }));
});
self.addEventListener("notificationclick", (event) => { event.notification.close();
  event.waitUntil(self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((clients) => {
    const target = new URL(event.notification.data?.url || "/phone", self.location.origin).href, open = clients.find((client) => client.url === target);
    return open ? open.focus() : self.clients.openWindow(target);
  }));
});
