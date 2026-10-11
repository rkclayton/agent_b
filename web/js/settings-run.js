let store, approvalChoices;
function useSettingsContext(context) {
  ({ store, approvalChoices } = context);
}

function run() {
  return approvalChoices(store.config.approval?.mode, "Choose when tool calls need approval.");
}


export function renderRunPage(context) {
  useSettingsContext(context);
  return run();
}
