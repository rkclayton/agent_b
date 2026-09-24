let store, connectionList, number, choices, row, html;
function useSettingsContext(context) {
  ({ store, connectionList, number, choices, row, html } = context);
}

function context(active) {
  const connection = connectionList().find((x) => x.id === active?.connection_id);
  const choice = store.config.context?.accounting || "auto";
  let actual = "estimated — no active connection";
  if (connection) {
    actual = choice === "estimated"
      ? "estimated — by choice"
      : connection.capabilities?.tokenize
        ? "exact — /tokenize available"
        : "estimated — no /tokenize on this connection";
  }
  const facts = store.serving_facts || {};
  const blocked = ["yes", "partial"].includes(facts.tokenize_blocks_on_slot);
  return `${number("context.soft_pct", "soft threshold (%)", Math.round((store.config.context?.soft_pct || 0) * 100), "1", false, "", false, "percent", "Share of the context ceiling at which older tool results are elided to stubs.")}
    ${number("context.summary_pct", "summary threshold (%)", Math.round((store.config.context?.summary_pct || 0) * 100), "1", false, "", false, "percent", "Share of the context ceiling at which older turns are folded into a progress note.")}
    ${choices("context.accounting", "accounting", ["auto", "exact", "estimated"], choice, "How context use is counted: exact asks the server's tokenizer, estimated counts characters, auto uses exact when the server offers it.")}
    <p class="settings-note">${html(actual)}</p>
    ${blocked ? `<p class="settings-note">/tokenize measured ${html(facts.tokenize_busy_ms || "?")} ms busy and may occupy the generation slot</p>` : ""}
    ${row("reserve", `<output>${active?.budget?.reserve ?? 0}</output>`, "", "Tokens kept free for the model's answer, from the active connection.")}
    ${row("ceiling", `<output>${active?.budget?.ceiling ?? 0}</output>`, "", "The most prompt the active connection accepts: its context size minus the reserve.")}
    <p class="settings-note">from connection ${html(connection?.label || "none")}</p>`;
}


export function renderContextPage(active, pageContext) {
  useSettingsContext(pageContext);
  return context(active);
}
