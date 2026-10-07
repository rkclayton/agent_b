let store, row, toggle, html, signInStart;
function useSettingsContext(context) {
  ({ store, row, toggle, html, signInStart } = context);
}
// The toggle remains the only control; the destination host is disclosure, not a setting.
function telemetry() {
  const config = store.config.telemetry || {};
  const on = config.enabled !== false;
  let destination = "broker.agentb.app";
  try { destination = new URL(config.endpoint || "https://broker.agentb.app/v1/telemetry").host; } catch { destination = "configured receiver"; }
  return `${toggle("telemetry.enabled", "Send anonymous data to help improve Agent_b", on)}<p class="settings-subhead-note">Only diagnostic data is sent — counts, durations and error classes. Never your chats, files or prompts. Sent to ${html(destination)}.</p>`;
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
  // Item 2nh (b): THE OUTCOME IS STATED WHERE THE CONTROL WAS.
  //
  // The operator's update to v1.29.0 worked, and he reported it as a failure: his
  // window closed under him and the new one came back on the old chat with nothing
  // saying what had happened. The instance reading this line IS the result of that
  // update, and the updater read the installer's own finish before this page loaded,
  // so the row can say so until the next check replaces it.
  const outcome = update.outcome || null;
  const outcomeAt = outcome?.at ? new Date(outcome.at) : null;
  const outcomeTime = outcomeAt && !Number.isNaN(outcomeAt.getTime())
    ? outcomeAt.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : "";
  // Item 2mk (a): AND THE VERSION THAT DID NOT CHANGE IS SAID IN THOSE WORDS. An
  // install can finish and leave this window serving the old build — it did, on
  // 2026-09-26, and the operator was eleven releases behind before a report told
  // him. (c): every one of these names the install it is about.
  const outcomeWhere = outcome?.application_root ? ` · ${outcome.application_root}` : "";
  const outcomeLine = !outcome ? ""
    : outcome.running
      ? `${outcome.version} was installed${outcomeTime ? ` at ${outcomeTime}` : ""}, but this window is still running ${outcome.running} — the version did not change here${outcomeWhere}`
      : outcome.ok
        ? `updated to ${outcome.version}${outcomeTime ? ` at ${outcomeTime}` : ""}${outcomeWhere}`
        : `update to ${outcome.version || "the new version"} failed at ${outcome.phase || "install"}: ${outcome.error}${outcome.transcript && !String(outcome.error || "").includes(outcome.transcript) ? ` · ${outcome.transcript}` : ""}${outcomeWhere}`;
  // (c): a warning is a note under the outcome, not the result. The migration line
  // the installer wrote after the finish is the one this exists for.
  const notes = Array.isArray(outcome?.warnings) ? outcome.warnings : [];
  const noteMarkup = notes.map((note) => `<span class="settings-update-note">${html(note)}</span>`).join("");
  // (a): ONE WAIT ELEMENT, and the line on it is the stage in hand — download with
  // bytes of total, verify, stopping, the installer's own phases, restart. The seat
  // is filled by settings.js, because the element is built, not written as markup.
  const waiting = update.installing || (update.step && !update.error);
  // (b): HOW FAR BEHIND, where he already looks. One published release is one thing
  // he did not get, so it is a count of releases and not a difference of versions.
  const behind = Number(update.releases_behind || 0);
  const behindText = behind > 1 ? ` · ${behind} releases behind` : behind === 1 ? " · 1 release behind" : "";
  // (d): an updater that is switched off says so rather than checking silently
  // forever. Check now still works, because asking once is not the same as asking
  // every hour.
  const status = update.enabled === false ? "update checks are off"
    : waiting ? (update.line || "starting the update")
    : update.checking ? "Checking…"
    : failure ? failure
    : update.available ? `${update.version} available${behindText}${update.notes ? ` · ${update.notes}` : ""}`
    : outcomeLine ? outcomeLine
    : update.checked_at ? "Up to date" : "Not checked yet";
  const installAction = update.available
    ? `<button type="button" data-action="install-update" ${update.installing ? "disabled" : ""}>${update.installing ? "Starting…" : "Update"}</button>`
    : "";
  const checked = update.checked_at ? new Date(update.checked_at).toLocaleString() : "never";
  const started = store.server_started_at ? new Date(store.server_started_at).toLocaleString() : "unknown", signInEnabled = !!signInStart?.enabled, signInError = signInStart?.error || "", signInSwitch = `<button type="button" role="switch" aria-checked="${signInEnabled}" aria-label="Start when I sign in" class="switch ${signInEnabled ? "on" : ""}" data-action="sign-in-start" ${signInStart?.busy || !signInStart?.loaded ? "disabled" : ""}></button>`;
  return `${row("version", `<code class="settings-build-text">${html(`${tag} · ${commit}${build.dirty ? " · dirty" : ""} · ${signatureWord}`)}</code>`, "", "Build identity and the running executable's signature status.")}
    ${row("server", `<span class="settings-server-value">server started <time class="settings-server-started">${html(started)}</time>, ${html(tag)}</span>`, "", "The process this window is attached to.")}
    ${toggle("updates.auto_check", "check for updates", store.config.updates?.auto_check !== false, "At startup, every hour, and on window attach (at most once per 15 minutes), sends one anonymous GET to api.github.com for someone/agent_b's latest release. It sends no Agent_b data.")}
    ${row("checked", `<span>checked <time class="settings-update-checked">${html(checked)}</time></span><button type="button" data-action="check-update" ${update.checking ? "disabled" : ""}>${update.checking ? "Checking…" : "Check now"}</button>`, update.error ? "invalid" : "", "The most recent completed release check.")}
    ${row("update", `<span${waiting ? ` data-update-wait="1" data-update-line="${attrOf(status)}" data-update-processed="${Number(update.processed || 0)}" data-update-total="${Number(update.total || 0)}"` : ""}>${html(status)}</span>${outcomeLine && update.available ? `<span class="settings-update-note">${html(outcomeLine)}</span>` : ""}${noteMarkup}${installAction}`, (update.error || outcome?.ok === false || outcome?.running) ? "invalid" : "", `An update is downloaded only when you press Update. Agent_b verifies release.json and the setup SHA-256 before starting the per-user installer without elevation. While it runs, the line names the stage in hand; when the app comes back, this row says what the update did. This installation is ${html(String(update.application_root || "at an unnamed path"))}.`)}
    ${row("Start when I sign in", `${signInSwitch}${signInError ? `<span class="settings-update-note">${html(signInError)}</span>` : ""}`, signInError ? "invalid" : "", "Uses the same Agent_b entry shown in Windows Settings → Apps → Startup. When on, sign-in opens the ordinary Agent_b window.")}
    ${row("diagnostics", `<span>one file describing this installation</span><button type="button" data-action="export-diagnostics">Export diagnostics</button>`, "", "Gathers what Agent_b already knows — build, update state, the last install attempt, connection test results and recent log lines — into one file. Nothing new is measured. Paths outside the installation, account names, addresses and anything token-shaped are replaced, so the file is safe to send to whoever is helping.")}
    ${telemetry()}`;
}
export function renderAboutPage(context) {
  useSettingsContext(context);
  return about();
}
