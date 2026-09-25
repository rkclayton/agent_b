const status = document.getElementById("phone-status"), enrol = document.getElementById("phone-enrol"), chat = document.getElementById("phone-chat");
const transcript = document.getElementById("phone-transcript"), chatName = document.getElementById("phone-chat-name"), message = document.getElementById("phone-message"), push = document.getElementById("phone-push");
let activeSession = "", refreshTimer = 0;
const registration = "serviceWorker" in navigator ? await navigator.serviceWorker.register("/phone-sw.js", { scope: "/" }) : null;
if (registration) await navigator.serviceWorker.ready;
function workerMessage(action, value = "") {
  const worker = navigator.serviceWorker.controller || registration?.active; if (!worker) return Promise.resolve("");
  return new Promise((resolve) => {
    const channel = new MessageChannel(); channel.port1.onmessage = (event) => resolve(event.data || "");
    worker.postMessage({ action, value }, [channel.port2]);
  });
}
async function api(path, options = {}) {
  const response = await fetch(path, { cache: "no-store", ...options }), body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(body.error || `HTTP ${response.status}`); error.status = response.status; throw error;
  }
  return body;
}
function line(entry) {
  const row = document.createElement("div"), role = document.createElement("span"), text = document.createElement("p"); row.className = "phone-line"; role.className = "phone-role"; text.className = "phone-text";
  if (entry.type === "user") { role.textContent = "You"; text.textContent = entry.text || ""; }
  else if (entry.type === "agent") { role.textContent = "Agent"; text.textContent = entry.text || ""; }
  else if (entry.type === "tool") { role.textContent = "Tool"; text.textContent = entry.content || entry.name || ""; }
  else { role.textContent = "Notice"; text.textContent = entry.event?.data?.human?.happened || entry.event?.type || ""; }
  if (!text.textContent) return null; row.append(role, text); return row;
}
function render(state) {
  const sessions = Object.values(state.sessions || {}).filter((item) => !item.closed), requested = new URLSearchParams(location.search).get("chat");
  const session = sessions.find((item) => item.id === requested) || sessions.find((item) => item.id === activeSession) || sessions.at(-1);
  activeSession = session?.id || ""; chatName.textContent = session?.label || "No open chat";
  transcript.replaceChildren();
  for (const entry of session?.chat || []) { const node = line(entry); if (node) transcript.append(node); }
  transcript.scrollTop = transcript.scrollHeight;
}
async function refresh() {
  clearTimeout(refreshTimer); try {
    const state = await api("/api/state"); render(state);
    enrol.hidden = true; chat.hidden = false;
    status.textContent = "connected"; status.className = "";
  } catch (error) {
    if (error.status === 401) {
      await workerMessage("credential.set", "");
      enrol.hidden = false; chat.hidden = true;
      status.textContent = "enter the desktop code"; return;
    }
    status.textContent = error.message; status.className = "alarm";
  }
}
document.getElementById("phone-enrol-form").addEventListener("submit", async (event) => {
  event.preventDefault(); try {
    const result = await api("/api/phone/enrolment/redeem", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ code: document.getElementById("phone-code").value, name: document.getElementById("phone-name").value }) });
    await workerMessage("credential.set", result.credential); location.reload();
  } catch (error) {
    status.textContent = error.status === 401 ? "code refused" : error.message; status.className = "alarm";
  }
});
document.getElementById("phone-composer").addEventListener("submit", async (event) => {
  event.preventDefault(); if (!activeSession || !message.value.trim()) return;
  const text = message.value; message.value = "";
  try { await api("/api/message", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ session_id: activeSession, text }) }); }
  catch (error) { message.value = text; status.textContent = error.message; status.className = "alarm"; }
});
async function refreshPush() {
  try {
    const state = await api("/api/phone/push"), subscription = await registration?.pushManager?.getSubscription();
    push.disabled = !state.enabled; push.textContent = subscription ? "Push on" : state.enabled ? "Enable push" : "Push off";
    push.setAttribute("aria-pressed", String(!!subscription));
  } catch {}
} push.addEventListener("click", async () => {
  try {
    const state = await api("/api/phone/push");
    if (!state.enabled || !state.public_key) throw new Error("Enable push in Settings → Security first.");
    if (Notification.permission !== "granted" && await Notification.requestPermission() !== "granted") throw new Error("Push permission was not granted.");
    let subscription = await registration.pushManager.getSubscription();
    if (!subscription) {
      const raw = state.public_key.replaceAll("-", "+").replaceAll("_", "/"), key = raw.padEnd(Math.ceil(raw.length / 4) * 4, "=");
      subscription = await registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: Uint8Array.from(atob(key), (value) => value.charCodeAt(0)) });
    }
    const value = subscription.toJSON();
    await api("/api/phone/push/subscriptions", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ endpoint: value.endpoint, keys: value.keys }) }); await refreshPush();
  } catch (error) { status.textContent = error.message; status.className = "alarm"; }
});
const stream = new EventSource("/api/events"); stream.onmessage = () => { clearTimeout(refreshTimer); refreshTimer = setTimeout(refresh, 50); };
stream.onerror = () => { status.textContent = "connection lost — retrying"; status.className = "alarm"; };
await refresh(); await refreshPush();
