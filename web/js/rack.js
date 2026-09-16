import { store } from "./bus.js";
const root = document.getElementById("rack"),
  count = document.getElementById("tool-count");
export function renderRack() {
  const s = store.sessions[store.active];
  root.replaceChildren();
  if (!s) {
    count.textContent = "";
    return;
  }
  const activity = s.activity || {};
  const visible = s.tools.filter((tool) => tool.calls || activity.active_tool === tool.name || activity.alarm_tool === tool.name);
  const total = s.tools.reduce((sum, tool) => sum + (tool.calls || 0), 0);
  count.textContent = `${total} calls`;
  if (!visible.length) {
    const empty = document.createElement("div");
    empty.className = "panel-empty";
    empty.textContent = "—";
    root.append(empty);
  }
  for (const tool of visible) {
    const row = document.createElement("div");
    row.className = "tool-row";
    const live = activity.active_tool === tool.name,
      alarm = activity.alarm_tool === tool.name;
    row.innerHTML = `<span class="lamp ${live ? "live" : ""} ${alarm ? "alarm" : ""}"></span><span></span><span class="tool-calls number"></span>`;
    row.children[1].textContent = tool.name;
    row.children[2].textContent = tool.calls || 0;
    root.append(row);
  }
}
