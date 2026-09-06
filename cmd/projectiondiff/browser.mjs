// Differential consumer for `projectiondiff -browser-trace`. It applies every
// recorded event through the real browser reducer and compares its domain state
// with hashes emitted by the Go projector after the same durable record.
import crypto from "node:crypto";
import fs from "node:fs";
import readline from "node:readline";

globalThis.document = {
  hidden: false,
  getElementById: () => null,
  addEventListener: () => {},
};
globalThis.window = { addEventListener: () => {} };
globalThis.EventSource = class {
  addEventListener() {}
};
// Timed alarm/compaction clearing is view state. Keep the immediate reducer
// result stable while the durable projection is compared synchronously.
globalThis.setTimeout = () => 0;

const { reduce, store } = await import("../../web/js/bus.js");

const active = new Map();
const disagreements = [];
const issueCounts = new Map();
let currentPath = "";
let records = 0;
let files = 0;
let incomplete = new Set();

function initialSession(path, id) {
  let value = {
    id,
    label: id,
    server_id: "",
    workspace: "",
    run: { status: "replay", run_id: "", turn: 0, max_turns: 0, queue_position: 0, partial: "", last_stop_reason: "" },
    tools: [],
    messages: [],
    budget: {},
    timeline: [],
    queued_messages: 0,
    runnable: true,
    not_runnable_reason: "",
    memory_path: "",
    memory_content: "",
    model_turns: 0,
    compaction_count: 0,
    compaction_token_delta: 0,
    compaction_model_calls: 0,
    compaction_prompt_tokens: 0,
    compaction_completion_tokens: 0,
  };
  for (const line of fs.readFileSync(path, "utf8").split(/\r?\n/)) {
    if (!line.trim()) continue;
    const event = JSON.parse(line);
    if (event.type !== "session.created" || !event.data?.session) continue;
    value = structuredClone(event.data.session);
    value.id = id;
    value.run = { ...(value.run || {}), status: "replay" };
    value.messages = [];
    value.timeline = [];
  }
  value.log_path = path;
  return value;
}

function reset(path, id) {
  reduce({
    type: "snapshot",
    data: {
      sessions: { [id]: initialSession(path, id) },
      active: id,
      replay: true,
      servers: [],
      config: {},
      flow: { stages: [], edges: [] },
      tools: [],
      serving_facts: {},
    },
  });
}

function toolField(values, pick) {
  return (values || []).map((tool) => [tool.name || "", pick(tool)]);
}

function int(value) {
  return Number.isFinite(value) ? value : 0;
}

function fields(value) {
  const run = value.run || {};
  const budget = value.budget || {};
  const activity = {
    stage: value._stage || "",
    stage_state: value._stageState || "",
    completed_stages: value._completedStages || [],
    active_tool: value._activeTool || "",
    alarm_tool: value._alarmTool || "",
    dispatch_alarm: !!value._dispatchAlarm,
  };
  if (value._progress != null) activity.progress = value._progress;
  if (value._timings != null) activity.last_timings = value._timings;
  return {
    identity: [value.id || "", value.label || "", value.server_id || "", value.workspace || ""],
    "run.status": run.status || "",
    "run.run_id": run.run_id || "",
    "run.turn": int(run.turn),
    "run.max_turns": int(run.max_turns),
    "run.queue_position": int(run.queue_position),
    "run.partial": run.partial || "",
    "run.last_stop_reason": run.last_stop_reason || "",
    "tools.order": toolField(value.tools, (tool) => tool.name || ""),
    "tools.enabled": toolField(value.tools, (tool) => !!tool.enabled),
    "tools.calls": toolField(value.tools, (tool) => int(tool.calls)),
    "tools.schema_tokens": toolField(value.tools, (tool) => int(tool.schema_tokens)),
    "tools.marginal_tokens": toolField(value.tools, (tool) => int(tool.marginal_tokens)),
    messages: value.messages || [],
    "budget.n_ctx": int(budget.n_ctx),
    "budget.reserve": int(budget.reserve),
    "budget.ceiling": int(budget.ceiling),
    "budget.used_est": int(budget.used_est),
    "budget.used_measured": int(budget.used_measured),
    "budget.drift": int(budget.drift),
    "budget.cached_last": budget.cached_last ?? null,
    "budget.mode": budget.mode || "",
    "budget.estimated": !!budget.estimated,
    "budget.estimated_categories": budget.estimated_categories || [],
    "budget.categories": budget.categories || {},
    "budget.tool_schema_tokens": budget.tool_schema_tokens || {},
    "budget.tool_marginal_tokens": budget.tool_marginal_tokens || {},
    queued_messages: int(value.queued_messages),
    runnable: [!!value.runnable, value.not_runnable_reason || ""],
    memory: [value.memory_path || "", value.memory_content || ""],
    model_turns: int(value.model_turns),
    compaction: [
      int(value.compaction_count),
      int(value.compaction_token_delta),
      int(value.compaction_model_calls),
      int(value.compaction_prompt_tokens),
      int(value.compaction_completion_tokens),
    ],
    activity,
  };
}

function stable(value) {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(stable).join(",")}]`;
  return `{${Object.keys(value).sort().filter((key) => value[key] !== undefined).map((key) => `${JSON.stringify(key)}:${stable(value[key])}`).join(",")}}`;
}

function digest(value) {
  const encoded = stable(value)
    .replaceAll("<", "\\u003c")
    .replaceAll(">", "\\u003e")
    .replaceAll("&", "\\u0026")
    .replaceAll("\u2028", "\\u2028")
    .replaceAll("\u2029", "\\u2029");
  return crypto.createHash("sha256").update(encoded).digest("hex");
}

function compact(value) {
  const encoded = stable(value);
  return encoded.length > 500 ? `${encoded.slice(0, 500)}…` : encoded;
}

const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
for await (const line of input) {
  if (!line.trim()) continue;
  const trace = JSON.parse(line);
  if (trace.path !== currentPath) {
    currentPath = trace.path;
    files++;
    active.clear();
    reset(trace.path, trace.event.session_id);
  }
  records++;
  if (!trace.complete) incomplete.add(trace.path);
  reduce(trace.event);
  const target = store.sessions[trace.event.session_id];
  if (!target) continue;
  const actual = fields(target);
  for (const [field, expectedHash] of Object.entries(trace.fields)) {
    const actualHash = digest(actual[field]);
    const key = field;
    const different = expectedHash !== actualHash;
    if (different && !active.get(key)) {
      disagreements.push({
        path: trace.path,
        consumer: "web/js/bus.js",
        field,
        generation: trace.cursor.generation,
        offset: trace.cursor.offset,
        seq: trace.event.seq || 0,
        event_type: trace.event.type,
        projected_hash: expectedHash,
        browser: compact(actual[field]),
      });
      const issue = `${field}\u0000${trace.event.type}`;
      issueCounts.set(issue, (issueCounts.get(issue) || 0) + 1);
    }
    active.set(key, different);
  }
}

console.log(JSON.stringify({
  files,
  records,
  incomplete_logs: [...incomplete].sort(),
  issue_counts: [...issueCounts.entries()].sort(([left], [right]) => left.localeCompare(right)).map(([key, count]) => {
    const [field, event_type] = key.split("\u0000");
    return { consumer: "web/js/bus.js", field, event_type, count };
  }),
  disagreements,
}, null, 2));
