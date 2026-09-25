let store, row, toggle, html;
function useSettingsContext(context) {
  ({ store, row, toggle, html } = context);
}

function about() {
  const build = store.build || {};
  const tag = build.tag ? (String(build.tag).startsWith("v") ? build.tag : `v${build.tag}`) : "version unknown";
  const commit = String(build.commit || "unknown").slice(0, 7);
  const signatureFiles = Array.isArray(store.signature?.files) ? store.signature.files : [];
  const signatureWord = signatureFiles.length > 0 && signatureFiles.every((file) => file.status === "Valid" && file.timestamped) ? "signed" : "unsigned";
  const update = store.update || {};
  const status = update.installing ? "Downloading and verifying…"
    : update.checking ? "Checking…"
    : update.available ? `${update.version} available${update.notes ? ` · ${update.notes}` : ""}`
    : update.error ? `Check failed · ${update.error}`
    : update.checked_at ? "Up to date" : "Not checked yet";
  const installAction = update.available
    ? `<button type="button" data-action="install-update" ${update.installing ? "disabled" : ""}>${update.installing ? "Starting…" : "Update"}</button>`
    : "";
  const checked = update.checked_at ? new Date(update.checked_at).toLocaleString() : "never";
  const started = store.server_started_at ? new Date(store.server_started_at).toLocaleString() : "unknown";
  return `${row("version", `<code class="settings-build-text">${html(`${tag} · ${commit}${build.dirty ? " · dirty" : ""} · ${signatureWord}`)}</code>`, "", "Build identity and the running executable's signature status.")}
    ${row("server", `<span class="settings-server-value">server started <time class="settings-server-started">${html(started)}</time>, ${html(tag)}</span>`, "", "The process this window is attached to.")}
    ${toggle("updates.auto_check", "check for updates", store.config.updates?.auto_check !== false, "At startup, every hour, and on window attach (at most once per 15 minutes), sends one anonymous GET to api.github.com for rkclayton/agent_b's latest release. It sends no Agent_b data.")}
    ${row("checked", `<span>checked <time class="settings-update-checked">${html(checked)}</time></span><button type="button" data-action="check-update" ${update.checking ? "disabled" : ""}>${update.checking ? "Checking…" : "Check now"}</button>`, update.error ? "invalid" : "", "The most recent completed release check.")}
    ${row("update", `<span>${html(status)}</span>${installAction}`, update.error ? "invalid" : "", "An update is downloaded only when you press Update. Agent_b verifies release.json and the setup SHA-256 before starting the per-user installer without elevation.")}`;
}


export function renderAboutPage(context) {
  useSettingsContext(context);
  return about();
}
