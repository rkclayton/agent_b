const producingTools = new Set(["write_file", "edit_file", "call_service"]);

export function filesFromResponse(items) {
  const callRuns = new Map();
  for (const item of items || []) {
    try {
      if (item?.type !== "agent") continue;
      for (const callID of item.toolCallIDs || []) callRuns.set(callID, item.run_id || "");
    } catch {}
  }
  const files = new Map();
  for (const item of items || []) {
    try {
      if (item?.type !== "tool" || !producingTools.has(item.name) || item.result?.ok !== true) continue;
      const recorded = item.result?.file;
      const path = recorded?.path || legacyRelativePath(item.args?.path);
      if (typeof path !== "string" || !path) continue;
      files.set(path.toLowerCase(), {
        path,
        bytes: Number.isFinite(recorded?.bytes) ? recorded.bytes : null,
        callID: item.callID || "",
        runID: callRuns.get(item.callID) || "",
        openScope: "workspace",
        openPath: path,
      });
    } catch {}
  }
  const delivery = (items || []).find((item) => {
    try { return item?.type === "notice" && item.event?.type === "files.delivered"; }
    catch { return false; }
  })?.event?.data;
  if (delivery?.mode === "folder") return [];
  if (delivery?.mode === "both") {
    for (const item of delivery.items || []) {
      if (!item.exchange_path || !["copied", "identical"].includes(item.status)) continue;
      const file = files.get(String(item.source_path || "").toLowerCase());
      if (!file) continue;
      file.openScope = "exchange";
      file.openPath = item.exchange_path;
    }
  }
  return [...files.values()];
}

function legacyRelativePath(value) {
  if (typeof value !== "string" || !value.trim()) return "";
  const path = value.replaceAll("\\", "/");
  if (path.startsWith("/") || path.startsWith("//") || /^[a-z]:\//i.test(path)) return "";
  const parts = path.split("/").filter((part) => part && part !== ".");
  if (!parts.length || parts.some((part) => part === "..")) return "";
  return parts.join("/");
}

export function fileURL(sessionID, path) {
  const encoded = path.split("/").map(encodeURIComponent).join("/");
  const query = new URLSearchParams();
  if (sessionID) query.set("session", sessionID);
  return `/api/files/${encoded}${query.size ? `?${query}` : ""}`;
}

export async function probeFile(url, fetcher = fetch) {
  try {
    const response = await fetcher(url, { method: "HEAD" });
    if (!response.ok) return { state: "missing", bytes: null };
    const length = Number(response.headers.get("content-length"));
    return { state: "ready", bytes: Number.isFinite(length) && length >= 0 ? length : null };
  } catch {
    return { state: "missing", bytes: null };
  }
}

export function attachmentDisplay(file = {}) {
  const basename = String(file.display_name || file.path || file.name || "attachment").split(/[\\/]/).at(-1);
  const extension = /\.([^.]+)$/.exec(String(file.path || file.name || basename))?.[1]?.toUpperCase() || "FILE";
  const pastedImage = basename === "Pasted image" || basename.toLowerCase().startsWith("pasted-attachment");
  const label = pastedImage ? "Pasted image" : basename;
  const pasted = pastedImage && file.width && file.height ? `${label} · ${file.width} × ${file.height}` : label;
  return { name: pasted, type: extension, image: file.kind === "image" || /^(PNG|JPG|JPEG|GIF|WEBP)$/.test(extension) };
}

export function createAttachmentBubble(document, file, options = {}, actions = {}) {
  const display = attachmentDisplay(file), root = document.createElement("span");
  root.className = "attachment-bubble chat-bubble";
  root.title = options.title || "";
  const preview = document.createElement(display.image ? "img" : "span");
  preview.className = display.image ? "attachment-bubble-preview" : "attachment-bubble-type";
  if (display.image) {
    preview.src = options.previewURL || fileURL(options.sessionID, file.path); preview.alt = "";
    if (display.name === "Pasted image") preview.onload = () => { name.textContent = `Pasted image · ${preview.naturalWidth} × ${preview.naturalHeight}`; };
  }
  else preview.textContent = display.type;
  const words = document.createElement("span"); words.className = "attachment-bubble-words";
  const name = document.createElement("span"); name.className = `attachment-bubble-name${actions.openFile ? " openable" : ""}`; name.textContent = display.name;
  if (actions.openFile) name.onclick = actions.openFile;
  words.append(name);
  if (options.state) { const state = document.createElement("span"); state.className = `attachment-bubble-state${options.alarm ? " alarm" : ""}`; state.textContent = options.state; words.append(state); }
  root.append(preview, words);
  if (options.removable && actions.remove) { const remove = document.createElement("button"); remove.type = "button"; remove.className = "attachment-bubble-remove"; remove.textContent = "×"; remove.setAttribute("aria-label", `Remove ${display.name}`); remove.onclick = actions.remove; root.append(remove); }
  if (actions.openFolder) { const folder = document.createElement("a"); folder.className = "file-chip-folder"; folder.innerHTML = FOLDER_GLYPH; folder.title = "folder"; folder.ariaLabel = "folder"; folder.href = "#"; folder.onclick = (event) => { event.preventDefault(); return actions.openFolder(); }; root.append(folder); }
  return root;
}

const FOLDER_GLYPH =
  '<svg viewBox="0 0 16 12" width="13" height="10" aria-hidden="true" focusable="false"><path d="M1 2.5V10.5a.5.5 0 0 0 .5.5h13a.5.5 0 0 0 .5-.5V4a.5.5 0 0 0-.5-.5H7.2L5.9 1.8a.5.5 0 0 0-.4-.3H1.5a.5.5 0 0 0-.5.5z" fill="none" stroke="currentColor" stroke-width="1" stroke-linejoin="round"/></svg>';

// The same document types the server's /api/open-file opens; anything whose
// default action runs it (scripts, executables) offers only its folder.
const OPENABLE = new Set([".xlsx", ".docx", ".pptx", ".pdf", ".txt", ".md", ".json", ".log", ".png", ".jpg", ".jpeg", ".gif", ".webp"]);
export function openableFile(path = "") {
  const match = /\.[^./\\]+$/.exec(String(path).toLowerCase());
  return !!match && OPENABLE.has(match[0]);
}

export function createFileChip(document, file, status, actions, presentation = {}) {
  if (file.attachment) return createAttachmentBubble(document, file, { sessionID: presentation.sessionID, state: status?.state === "missing" ? "missing" : presentation.state, alarm: presentation.alarm, title: presentation.title || file.note || file.outcome }, actions);
  const root = document.createElement("div");
  root.className = `file-chip${status?.state === "missing" ? " missing" : ""}`;
  const name = document.createElement("span");
  name.className = "file-chip-name";
  name.textContent = file.path.split("/").at(-1);
  name.title = file.path;
  const size = document.createElement("span");
  size.className = "file-chip-size";
  size.textContent = status?.state === "missing" ? "missing" : formatBytes(status?.bytes ?? file.bytes);
  root.append(name, size);
  // Item 2ep: a delivered document opens with the operator's default
  // application from its name; the folder is a glyph, "folder" on hover.
  if (status?.state !== "missing" && actions.openFile && openableFile(file.path)) {
    name.className = "file-chip-name openable";
    name.title = `open ${file.path}`;
    name.onclick = (event) => {
      event?.preventDefault?.();
      return actions.openFile();
    };
  }
  if (status?.state !== "missing") {
    const folder = document.createElement("a");
    folder.className = "file-chip-folder";
    folder.innerHTML = FOLDER_GLYPH;
    folder.title = "folder";
    folder.ariaLabel = "folder";
    folder.href = "#";
    folder.onclick = (event) => {
      event.preventDefault();
      return actions.openFolder();
    };
    root.append(folder);
  }
  return root;
}

export function formatBytes(value) {
  if (!Number.isFinite(value) || value < 0) return "size unknown";
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(value < 10 * 1024 ? 1 : 0)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(value < 10 * 1024 * 1024 ? 1 : 0)} MiB`;
}
