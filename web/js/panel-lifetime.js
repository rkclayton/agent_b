export function agentKey(agent) {
  return String(agent?.name || "").trim().toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

export function ratio(part, whole) {
  return whole > 0 ? `${(100 * part / whole).toFixed(1)}%` : "0.0%";
}

export function duration(milliseconds) {
  const ms = Number(milliseconds || 0);
  if (ms < 1000) return `${ms} ms`;
  if (ms < 60000) return `${(ms / 1000).toFixed(1)} s`;
  return `${(ms / 60000).toFixed(1)} min`;
}

export function compactionFigures(session = {}) {
  return `${Number(session.compaction_count || 0)} compactions · ${Number(session.compaction_model_calls || 0)} summaries`;
}

export function lifetimeRows(counters = {}, percentile = () => 0) {
  const reliability = counters.worker_reliability || {};
  const briefs = Number(reliability.briefs || counters.runs || 0);
  const tokens = Number(counters.prompt_tokens || 0) + Number(counters.completion_tokens || 0);
  const responses = counters.model_response_ms || [];
  return [
    ["chats", counters.chats || 0], ["runs / briefs", counters.runs || 0], ["turns", counters.turns || 0],
    ["approvals raised / approved", `${counters.approvals_raised || 0} / ${counters.approvals_approved || 0}`],
    ["operator grants", counters.operator_grants || 0], ["compactions", counters.compactions || 0],
    ["prompt / completion tokens", `${counters.prompt_tokens || 0} / ${counters.completion_tokens || 0}`],
    ["cache-hit ratio", ratio(counters.cached_tokens || 0, counters.prompt_tokens || 0)],
    ["wall time", duration(counters.wall_ms)], ["model response p50 / max", `${duration(percentile(responses, .5))} / ${duration(Math.max(0, ...responses))}`],
    ["first / last use", `${counters.first_use || "—"} / ${counters.last_use || "—"}`],
    ["completion rate", ratio(reliability.completed || 0, briefs)],
    ["interventions / brief", briefs ? (Number(reliability.interventions || 0) / briefs).toFixed(2) : "0.00"],
    ["cost / brief", briefs ? `${(Number(counters.turns || 0) / briefs).toFixed(2)} turns · ${Math.round(tokens / briefs)} tokens · ${duration(Number(counters.wall_ms || 0) / briefs)} · ${ratio(counters.cached_tokens || 0, counters.prompt_tokens || 0)} cache` : "0 turns · 0 tokens · 0 ms · 0.0% cache"],
    ["failure attribution model | harness | brief", `${reliability.model_failures || 0} | ${reliability.harness_failures || 0} | ${reliability.brief_failures || 0}`],
    ["rework rate", ratio(reliability.reworked || 0, briefs)], ["silence rate", ratio(reliability.silent || 0, briefs)],
  ];
}

// Item 2ji (c): the per-connection row, lifetime beside the last twenty runs.
//
// Lifetime alone cannot show that a connection has GOT WORSE, and that is the
// question the operator asks of this table. Two columns answer it; one does not.
// Density rules are kept: text only, no bar, no pane, no chart.

export const recentRunWindow = 20;

function median(values) {
  const sorted = [...(values || [])].map(Number).filter(Number.isFinite).sort((a, b) => a - b);
  if (!sorted.length) return null;
  const middle = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[middle] : Math.round((sorted[middle - 1] + sorted[middle]) / 2);
}

// A share of nothing is not zero per cent, it is nothing. Every figure here can
// be absent, and absent reads "—" rather than a confident 0.0%.
function share(part, whole) {
  return whole > 0 ? String(Math.round(100 * Number(part || 0) / whole)) : "—";
}

function rate(part, whole) {
  return whole > 0 ? `${(100 * Number(part || 0) / whole).toFixed(1)}%` : "—";
}

// runProfile reduces one side of the row -- lifetime, or the recent window -- to
// the figures item 2ji (c) names.
export function runProfile(counters = {}, recent = false) {
  if (recent) {
    const runs = counters.recent_runs || [];
    const totals = runs.reduce((sum, run) => ({
      total: sum.total + Number(run.total_ms || 0),
      model: sum.model + Number(run.model_ms || 0),
      tool: sum.tool + Number(run.tool_ms || 0),
      waiting: sum.waiting + Number(run.waiting_ms || 0),
      calls: sum.calls + Number(run.tool_calls || 0),
      failures: sum.failures + Number(run.tool_failures || 0),
      empty: sum.empty + Number(run.empty_replies || 0),
      repeated: sum.repeated + Number(run.repeated_calls || 0),
    }), { total: 0, model: 0, tool: 0, waiting: 0, calls: 0, failures: 0, empty: 0, repeated: 0 });
    return {
      runs: runs.length,
      median: median(runs.map((run) => run.total_ms)),
      wall: totals.total,
      model: totals.model, tool: totals.tool, waiting: totals.waiting,
      toolCalls: totals.calls, toolFailures: totals.failures,
      empty: totals.empty, repeated: totals.repeated,
    };
  }
  const times = counters.run_time_ms || [];
  const tools = Object.values(counters.tools || {});
  return {
    runs: times.length,
    median: median(times),
    wall: times.reduce((sum, value) => sum + Number(value || 0), 0),
    model: Number(counters.run_model_ms || 0),
    tool: Number(counters.run_tool_ms || 0),
    waiting: Number(counters.run_waiting_ms || 0),
    toolCalls: tools.reduce((sum, tool) => sum + Number(tool.calls || 0), 0),
    toolFailures: tools.reduce((sum, tool) => sum + Number(tool.failures || 0), 0),
    empty: Number(counters.empty_replies || 0),
    repeated: Number(counters.repeated_calls || 0),
  };
}

// runProfileRows is [label, lifetime, recent]. It returns nothing at all when no
// run has ever reported its time, which is every ledger written before item 2ji
// -- an empty column is more honest than a column of dashes.
export function runProfileRows(counters = {}) {
  const life = runProfile(counters, false);
  const recent = runProfile(counters, true);
  if (!life.runs && !recent.runs) return [];
  const pair = (label, read) => [label, read(life), read(recent)];
  return [
    pair("runs measured", (p) => String(p.runs)),
    pair("median run", (p) => (p.median === null ? "—" : duration(p.median))),
    // The per cent signs are dropped: the label says these are shares, and with
    // them the row wrapped over three lines in Activity's narrow columns.
    pair("model / tools / waiting %", (p) => `${share(p.model, p.wall)}/${share(p.tool, p.wall)}/${share(p.waiting, p.wall)}`),
    pair("empty replies / run", (p) => rate(p.empty, p.runs)),
    pair("tool errors", (p) => rate(p.toolFailures, p.toolCalls)),
    pair("repeated calls / run", (p) => rate(p.repeated, p.runs)),
  ];
}
