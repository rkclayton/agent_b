let store, row, toggle, html;
function useSettingsContext(context) {
  ({ store, row, toggle, html } = context);
}

function about() {
  const build = store.build || {};
  const tag = build.tag ? (String(build.tag).startsWith("v") ? build.tag : `v${build.tag}`) : "version unknown";
  const commit = String(build.commit || "unknown").slice(0, 7);
  const update = store.update || {};
  const status = update.installing ? "Downloading and verifying…"
    : update.checking ? "Checking…"
    : update.available ? `${update.version} available${update.notes ? ` · ${update.notes}` : ""}`
    : update.error ? `Check failed · ${update.error}`
    : update.checked_at ? "Up to date" : "Not checked yet";
  const action = update.available
    ? `<button type="button" data-action="install-update" ${update.installing ? "disabled" : ""}>${update.installing ? "Starting…" : "Update"}</button>`
    : "";
  return `${row("version", `<code>${html(`${tag} · ${commit}${build.dirty ? " · dirty" : ""}`)}</code>`, "", "Build and signing details are kept in Settings so the shared application shell stays focused on selection and run state.")}
    ${toggle("updates.auto_check", "check for updates", store.config.updates?.auto_check !== false, "At startup and once a day, sends one anonymous GET to api.github.com for rkclayton/agent_b's latest release. It sends no Agent_b data.")}
    ${row("update", `<span>${html(status)}</span>${action}`, update.error ? "invalid" : "", "An update is downloaded only when you press Update. Agent_b verifies release.json and the setup SHA-256 before starting the installer; Windows asks once for elevation.")}`;
}


export function renderAboutPage(context) {
  useSettingsContext(context);
  return about();
}
