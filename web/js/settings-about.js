let store, row, toggle, html;
function useSettingsContext(context) {
  ({ store, row, toggle, html } = context);
}

// Item 2jg (d): the switch, one line of what is and is not sent, and the last
// batches exactly as they left. The last of those is the point: the claim above
// it can be checked rather than believed.
function telemetry() {
  const config = store.config.telemetry || {};
  const on = config.enabled !== false;
  const batches = Array.isArray(store.telemetry_sent) ? store.telemetry_sent : [];
  const endpoint = String(config.endpoint || "");
  const state = !endpoint ? "no receiver configured — nothing is collected"
    : on ? `sending to a receiver you run`
    : "off — nothing is collected and the queue is deleted";
  const sent = !batches.length
    ? row("what was sent", `<span class="settings-telemetry-empty">nothing yet</span>`, "", "The last 20 batches, exactly as they left this machine.")
    : row("what was sent", `<span class="settings-telemetry-list">${batches.slice(-20).reverse().map((batch) => `<span class="settings-telemetry-batch" title="${attrOf(batch.body || "")}">${html(batch.sent_at || "")} · ${Number(batch.events || 0)} events · ${Number(batch.bytes || 0)} B · ${batch.ok ? "sent" : "queued"}</span>`).join("")}</span>`, "", "The last 20 batches, exactly as they left this machine.");
  return `${toggle("telemetry.enabled", "send diagnostic telemetry", on, "Counts, durations and error classes only — never message text, tool output, paths, hostnames or file names. Off detaches the collector and deletes the queue; it does not send less. Turning it on issues a new install id.")}
    ${row("telemetry", `<span>${html(state)}</span>`, "", "The receiver is a service you run; Agent_b ships only the sender.")}
    ${sent}`;
}

function attrOf(value) {
  return String(value).replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}

function about() {
  const build = store.build || {};
  const tag = build.tag ? (String(build.tag).startsWith("v") ? build.tag : `v${build.tag}`) : "version unknown";
  const commit = String(build.commit || "unknown").slice(0, 7);
  const signatureFiles = Array.isArray(store.signature?.files) ? store.signature.files : [];
  const signatureWord = signatureFiles.length > 0 && signatureFiles.every((file) => file.status === "Valid" && file.timestamped) ? "signed" : "unsigned";
  const update = store.update || {};
  // Item 2mr (c): AN ERROR IS SHOWN WHETHER OR NOT AN UPDATE IS STILL OFFERED.
  //
  // The operator pressed Update, saw "downloading and verifying", and was returned
  // to "v1.24.0 available" with nothing said. This line is why: the error branch
  // sat BELOW the available branch, so a failed install — which leaves the update
  // available, because it is still available — could never reach it. The reason is
  // the updater's own words and it stays until the next check replaces it.
  const failure = update.error
    ? update.available
      // The version stays named in the line, so the reason does not cost the
      // reader the one fact the line used to carry. No new control is added.
      ? `${update.version} available · update failed · ${update.error}`
      : `Check failed · ${update.error}`
    : "";
  const status = update.installing ? "Downloading and verifying…"
    : update.checking ? "Checking…"
    : failure ? failure
    : update.available ? `${update.version} available${update.notes ? ` · ${update.notes}` : ""}`
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
    ${row("update", `<span>${html(status)}</span>${installAction}`, update.error ? "invalid" : "", "An update is downloaded only when you press Update. Agent_b verifies release.json and the setup SHA-256 before starting the per-user installer without elevation.")}
    ${telemetry()}`;
}


export function renderAboutPage(context) {
  useSettingsContext(context);
  return about();
}
