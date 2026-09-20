// Item 17-i: Console's reflection section. Read-only text — the latest
// overview and the latest tool-candidate report — fetched when Console opens
// and again after a run closes. It adds no control.

const stamp = (value) => {
  if (!value) return "";
  const at = new Date(value);
  return Number.isNaN(at.getTime()) ? "" : `${at.toISOString().replace("T", " ").slice(0, 16)} UTC`;
};

// reflectionLabel is the caption beside the section's title.
export function reflectionLabel(answer = {}) {
  if (!answer.enabled) return "off on this install";
  const parts = [];
  if (answer.at) parts.push(`overview ${stamp(answer.at)}`);
  if (answer.tier) parts.push(answer.tier);
  if (answer.report_at) parts.push(`report ${stamp(answer.report_at)}`);
  return parts.join(" · ") || "after each run, and daily";
}

// reflectionText is what the two panes show for an answer.
export function reflectionText(answer = {}) {
  if (!answer.enabled) return { overview: "Reflection is off on this install.", report: "" };
  return { overview: answer.overview || "No reflection pass has run yet.", report: answer.report || "" };
}

export async function loadReflection(fetcher = fetch, doc = globalThis.document) {
  const overview = doc?.getElementById("console-reflection-overview");
  if (!overview) return null;
  let answer;
  try {
    const response = await fetcher("/api/reflection");
    if (!response.ok) throw new Error(String(response.status));
    answer = await response.json();
  } catch {
    return null;
  }
  const text = reflectionText(answer);
  overview.textContent = text.overview;
  const report = doc.getElementById("console-reflection-report");
  if (report) report.textContent = text.report;
  const when = doc.getElementById("console-reflection-when");
  if (when) when.textContent = reflectionLabel(answer);
  return answer;
}
