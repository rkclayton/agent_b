export function createMessageDropController(button, options) {
  let pending = false;

  function render() {
    const session = options.session();
    const interactive = options.interactive ? options.interactive() : true;
    const idle = session && ["idle", "replay"].includes(session.run?.status);
    const hasMessages = !!session?.messages?.length;
    button.disabled = pending || !interactive || !idle || !hasMessages;
    button.dataset.pending = String(pending);
    button.setAttribute("aria-busy", String(pending));
    button.title = hasMessages
      ? "Permanently remove the most recent conversation message"
      : "No conversation message to remove";
  }

  async function drop() {
    const session = options.session();
    if (pending || !session || (options.interactive && !options.interactive())) return;
    if (!session.messages?.length || !["idle", "replay"].includes(session.run?.status)) return;
    const message = session.messages[session.messages.length - 1];
    const detail = `Drop the most recent ${message.role || "conversation"} message from “${session.label || session.id}”?\n\nThis cannot be undone.`;
    if (!options.confirmDrop(detail)) return;
    pending = true;
    render();
    try {
      await options.drop(session.id);
    } catch (error) {
      options.reportError(error instanceof Error ? error.message : String(error));
    } finally {
      pending = false;
      render();
    }
  }

  button.addEventListener("click", drop);
  render();
  return { render, drop };
}
