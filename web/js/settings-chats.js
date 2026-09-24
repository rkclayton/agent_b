import { renderContextPage } from "./settings-context.js";
import { renderRunPage } from "./settings-run.js";

export function renderChatsPage(active, context) {
  return `${context.subhead("Run and approval", "Limits and approval behavior shared by every chat.")}
    ${renderRunPage(context)}
    ${context.number("run.max_concurrent", "max concurrent", context.store.config.run?.max_concurrent, "1", false, "", false, "number", "How many chats may run at the same time; the rest wait in the queue.")}
    ${context.subhead("Context", "Compaction thresholds and the active connection's measured limits.")}
    ${renderContextPage(active, context)}`;
}
