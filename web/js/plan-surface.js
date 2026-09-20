export const hintTexts = Object.freeze({
  accepted: "Cubes at the top track progress. Click one to reopen an item.",
  worker: "The worker runs this in a fresh session. You'll be asked only if it needs you.",
  apiAux: "Side jobs (compaction, naming) run on your API model. A small local Agent C does them for free. Assign one in Settings under Agents.",
  noPlanner: "No planner assigned; agent_b is planning. Reviews are off.",
});

export function claimHint(storage, id) {
  const key = `agentb.plan.hint.${id}`;
  if (!hintTexts[id] || storage.getItem(key)) return "";
  storage.setItem(key, "1");
  return hintTexts[id];
}

export function planRows(text = "") {
  const lines = String(text).replaceAll("\r", "").split("\n");
  // The current order is a "Current work order" heading at level 2 (the
  // harness form, optionally suffixed) or 3 (inside a new plan's milestones).
  const order = section(lines, /^(#{2,3})\s+Current work order\b/);
  const items = [];
  lines.forEach((line, index) => {
    const match = line.match(/^\s*\[([x~ !-])\]\s+(.+)$/);
    // An item's [[id]] is its identity for the harness, not text for the operator.
    const id = match?.[2].match(/^\[\[([0-9]+[a-z]*)\]\]\s*/);
    if (match) items.push({ marker: match[1], text: id ? match[2].slice(id[0].length) : match[2], id: id?.[1] || "", line, index });

  });
  return { order, items };
}

export function noteReports(text = "") {
  const lines = String(text).replaceAll("\r", "").split("\n");
  const reports = [];
  for (let index = 0; index < lines.length; index++) {
    if (!lines[index].startsWith("## ")) continue;
    let stop = lines.findIndex((line, candidate) => candidate > index && line.startsWith("## "));
    if (stop < 0) stop = lines.length;
    reports.push({ heading: lines[index].slice(3).trim(), body: lines.slice(index + 1, stop).join("\n").trim() });
  }
  return reports;
}

function section(lines, heading) {
  const start = lines.findIndex((line) => heading.test(line.trim()));
  if (start < 0) return "";
  const level = lines[start].trim().match(heading)[1].length;
  let end = lines.findIndex((line, index) => index > start && (line.match(/^(#{1,6})\s/)?.[1].length ?? 7) <= level);
  if (end < 0) end = lines.length;
  return lines.slice(start + 1, end).join("\n").trim();
}

// workerProposals are what the worker asked the planner for: an item that names
// no verifier. They arrive as c.job notices, in the design thread or, with no
// planner bound, on the worker itself, and only the planner can complete them.
export function workerProposals(sessions, session) {
  const threads = [session, ...Object.values(sessions || {}).filter((item) => item?.role === "c" && session?.plan_id && item.plan_id === session.plan_id)];
  const seen = new Set();
  const result = [];
  for (const thread of threads) {
    for (const entry of thread?.chat || []) {
      const proposal = entry?.event?.type === "c.job" ? entry.event.data?.proposal : null;
      if (!proposal || proposal.kind !== "verifier") continue;
      const key = proposalKey(proposal);
      if (seen.has(key)) continue;
      seen.add(key);
      result.push(proposal);
    }
  }
  return result;
}

export function proposalLabel(proposal) { return `${proposal.kind} · ${proposal.path} · ${proposal.item_id}`; }
export function proposalKey(proposal) { return JSON.stringify([proposal.id, proposal.kind, proposal.path, proposal.old_text, proposal.new_text, proposal.item_id]); }

// Item 2fc: the Plan page's pure parts, kept here so they can be tested.

// changedLines are the lines of next that were not in previous, counted as a
// multiset so a repeated line is new only when it appears more often.
export function changedLines(previous = "", next = "") {
  const counts = new Map();
  for (const line of String(previous).replaceAll("\r", "").split("\n")) counts.set(line, (counts.get(line) || 0) + 1);
  const changed = [];
  for (const line of String(next).replaceAll("\r", "").split("\n")) {
    const left = counts.get(line) || 0;
    if (left > 0) counts.set(line, left - 1);
    else if (line.trim()) changed.push(line);
  }
  return changed;
}

// fadeFor is a changed line's highlight strength: full when it changed, gone
// after an hour, and nothing for a line that has not changed since opening.
export const fadeWindowMS = 60 * 60 * 1000;
export function fadeFor(changedAt, now = Date.now()) {
  if (!changedAt) return 0;
  return Math.max(0, 1 - (now - changedAt) / fadeWindowMS);
}

export function filterPlans(plans = [], query = "") {
  const needle = String(query).trim().toLowerCase();
  return needle ? plans.filter((plan) => `${plan.name || ""} ${plan.id}`.toLowerCase().includes(needle)) : plans;
}

export function markerSummary(markers = {}) {
  const parts = [["open", "open"], ["done", "done"], ["stuck", "stuck"], ["partial", "partial"], ["dropped", "dropped"]]
    .filter(([key]) => markers?.[key]).map(([key, label]) => `${markers[key]} ${label}`);
  return parts.join(" · ") || "no items";
}
