let store, toggle, number, approvalChoices;
function useSettingsContext(context) {
  ({ store, toggle, number, approvalChoices } = context);
}

function run() {
  const cfg = store.config;
  return `${toggle("chat.auto_rename", "auto-name chats every 20 turns", cfg.chat?.auto_rename !== false)}
    ${number("run.cycle_window", "cycle window", cfg.run?.cycle_window, "1", false, "", false, "number", "0 = off")}
    ${number("run.max_consecutive_tool_errors", "max tool errors", cfg.run?.max_consecutive_tool_errors, "1", false, "", false, "number", "0 = off")}
    ${approvalChoices(cfg.approval?.mode, "With the service identity enabled, run_script still requires confirmation. Shell follows the approval mode; boundary-only runs in-folder commands silently, while boundary escapes and configured operator commands still ask.")}
	${number("run.queue_depth", "queue depth (0 = unbounded)", cfg.run?.queue_depth)}`;
}


export function renderRunPage(context) {
  useSettingsContext(context);
  return run();
}
