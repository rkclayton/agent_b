import { store } from "./bus.js";
import { percentClass } from "./percent.js";
import { contextOccupancy } from "./context-occupancy.js";

const root = document.getElementById("rail");
const compactionSeen = new Map();
const compactionPulseUntil = new Map();
export function renderRail() {
  const s = store.sessions[store.active];
  if (!s) {
    root.replaceChildren();
    return;
  }
  const b = s.budget || {},
    occupancy = contextOccupancy(b),
    n = Math.max(1, occupancy.nctx || 1),
    used = occupancy.occupied,
    ratio = used / Math.max(1, b.ceiling || 1);
  const serial = Number(s.activity?.compaction_serial || 0);
  if (compactionSeen.has(s.id) && compactionSeen.get(s.id) !== serial) compactionPulseUntil.set(s.id, Date.now() + 160);
  compactionSeen.set(s.id, serial);
  const compacted = Number(compactionPulseUntil.get(s.id) || 0) > Date.now();
  const meter = document.createElement("div");
  meter.className = `meter ${ratio > 0.85 ? "warn" : ""} ${ratio > 1 ? "over" : ""} ${compacted ? "compacted" : ""}`;
  meter.setAttribute("role", "img");
  const labels = document.createElement("div");
  labels.className = "rail-labels";
  const descriptions = [];
  for (const item of occupancy.items) {
    const wide = (item.value / n) * 100,
      segment = document.createElement("span"),
      marker = item.estimated ? "~" : "";
    segment.className = `${item.key === "reserve" ? "reserve" : "segment"} occupancy-${item.key} ${percentClass(wide)} ${item.estimated ? "estimated" : ""}`;
    segment.title = `${item.label} ${marker}${number(item.value)} (${marker}${percent(wide)})`;
    descriptions.push(segment.title);
    meter.append(segment);
    const label = document.createElement("span");
    label.className = "rail-label";
    label.append(`${item.label} `);
    const value = document.createElement("span");
    value.className = "number";
    value.textContent = `${marker}${percent(wide)}`;
    label.append(value);
    labels.append(label);
  }
  meter.setAttribute("aria-label", `Context occupancy: ${descriptions.join(", ")}`);
  const tick = document.createElement("span");
  tick.className = `ceiling-tick ${percentClass(((b.ceiling || 0) / n) * 100)}`;
  meter.append(tick);
  const edge = document.createElement("span");
  edge.className = `fill-edge ${percentClass((used / n) * 100)}`;
  meter.append(edge);
  const readout = document.createElement("div");
  readout.className = "rail-readout";
  const primary = document.createElement("span");
  primary.textContent = `${occupancy.estimated ? "~" : ""}${number(used)} / ${number(b.ceiling || 0)}`;
  readout.append(primary);
  if (b.used_measured) {
    const drift = document.createElement("span");
    drift.className = "rail-drift";
    drift.textContent = ` ${signed(b.drift || 0)}`;
    readout.append(drift);
  }
  const profile = store.servers.find((x) => x.id === s.server_id);
  if (
    profile?.capabilities?.cached_tokens &&
    b.cached_last !== null &&
    b.cached_last !== undefined
  )
    readout.append(`  cached ${number(b.cached_last)}`);
  const caption = document.createElement("div");
  caption.className = "rail-caption";
  caption.textContent = occupancy.estimated
    ? "context occupancy (estimated)"
    : "context occupancy";
  root.replaceChildren(meter, readout, labels, caption);
}
const number = (value) => new Intl.NumberFormat().format(value || 0);
const percent = (value) => `${Number(value || 0).toFixed(1)}%`;
const signed = (value) =>
  `${value < 0 ? "−" : value > 0 ? "+" : "±"}${number(Math.abs(value))}`;
