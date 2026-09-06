export function createSessionResetController(button, options) {
  let pending = false;

  function render() {
    const session = options.session();
    const interactive = options.interactive ? options.interactive() : true;
    button.disabled = pending || !interactive || !session;
    button.dataset.pending = String(pending);
    button.setAttribute("aria-busy", String(pending));
    button.title = session
      ? "Clear messages and run counters; keep workspace, profile, enabled tools, and memory"
      : "No conversation to clear";
  }

  async function clear() {
    const session = options.session();
    if (pending || !session || (options.interactive && !options.interactive())) return;
    const detail = `Clear conversation “${session.label || session.id}”?\n\nThis permanently clears its messages and run counters. Its workspace files, profile, enabled tools, and memory remain.`;
    if (!options.confirmClear(detail)) return;
    pending = true;
    render();
    try {
      const force = !["idle", "replay"].includes(session.run?.status);
      await options.reset(session.id, force);
    } catch (error) {
      options.reportError(error instanceof Error ? error.message : String(error));
    } finally {
      pending = false;
      render();
    }
  }

  button.addEventListener("click", clear);
  render();
  return { render, clear };
}
