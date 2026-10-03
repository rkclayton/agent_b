let store, toggle, number, approvalChoices;
function useSettingsContext(context) {
  ({ store, toggle, number, approvalChoices } = context);
}

// Item 2ls (b): the floor is one row, visible and adjustable, because a number
// taken from one machine's distribution should not be a constant the user
// cannot see. The default is item 2iy's measurement: three model calls or one
// tool call, whichever a run reaches first.
function reflectionFloor(cfg) {
  const floor = cfg.reflection?.floor || {};
  const hint = "A finished run below BOTH counts is not summarised, which is where reflection's cost went: most runs are short and summarising them doubled their cost. A run that ended badly is always summarised whatever its size. 0 on a count turns that half off; 0 on both summarises everything.";
  return `${number("reflection.floor.model_calls", "reflection floor: model calls", floor.model_calls, "1", false, "", false, "number", hint)}
    ${number("reflection.floor.tool_calls", "reflection floor: tool calls", floor.tool_calls, "1", false, "", false, "number", hint)}`;
}

function run() {
  const cfg = store.config;
  return `${number("run.cycle_window", "cycle window", cfg.run?.cycle_window, "1", false, "", false, "number", "0 = off")}
    ${number("run.max_consecutive_tool_errors", "max tool errors", cfg.run?.max_consecutive_tool_errors, "1", false, "", false, "number", "0 = off")}
    ${approvalChoices(cfg.approval?.mode, "With the service identity enabled, run_script still requires confirmation. Shell follows the approval mode; boundary-only runs in-folder commands silently, while boundary escapes and configured Run as you commands still ask.")}
	${number("run.queue_depth", "queue depth (0 = unbounded)", cfg.run?.queue_depth, "1", false, "", false, "number", "How many messages may wait behind a running chat before a new one is refused.")}
    ${reflectionFloor(cfg)}`;
}


export function renderRunPage(context) {
  useSettingsContext(context);
  return run();
}
