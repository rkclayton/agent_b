const menus = new Set();
const roots = new WeakSet();

const live = (entry) => entry.menu.isConnected !== false && !entry.menu.hidden;
const close = (entry) => {
  if (!live(entry)) return;
  entry.menu.hidden = true;
  entry.onClose?.();
};

function install(root) {
  if (roots.has(root)) return;
  roots.add(root);
  root.addEventListener("pointerdown", (event) => {
    for (const entry of menus) {
      if (!live(entry)) continue;
      if (!entry.menu.contains(event.target) && !entry.anchor?.contains(event.target)) close(entry);
    }
  }, true);
  root.addEventListener("click", (event) => {
    for (const entry of menus) if (live(entry) && entry.menu.contains(event.target) && event.target.closest?.("button, a") && !event.target.closest?.("[data-menu-reveal]")) close(entry);
  });
  root.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    const open = [...menus].filter(live);
    if (!open.length) return;
    open.forEach(close);
    event.preventDefault();
    event.stopImmediatePropagation?.();
  });
  root.defaultView?.addEventListener("blur", () => {
    for (const entry of menus) close(entry);
  });
}

export function registerMenu(menu, { anchor = null, onClose = null, root = document } = {}) {
  for (const old of menus) if (old.menu.isConnected === false) menus.delete(old);
  const entry = { menu, anchor, onClose };
  menus.add(entry);
  install(root);
  const open = () => {
    for (const other of menus) if (other !== entry) close(other);
    menu.hidden = false;
  };
  if (!menu.hidden) open();
  return { open, close: () => close(entry), toggle: () => menu.hidden ? open() : close(entry) };
}

let activeQuestion = null;

function questionBox({ message, anchor, className = "", input = null, confirmLabel = "Confirm", danger = false }) {
  activeQuestion?.cancel();
  const box = document.createElement("div");
  box.className = `confirm-popover ${className}`.trim();
  box.setAttribute("role", "dialog"); box.setAttribute("aria-modal", "false"); box.setAttribute("aria-label", input ? input.label : "Confirm");
  const words = document.createElement("p"); words.textContent = message; box.append(words);
  let field = null;
  if (input) { field = document.createElement("input"); field.value = input.value || ""; field.setAttribute("aria-label", input.label); box.append(field); }
  const actions = document.createElement("div"); actions.className = "confirm-actions";
  const cancel = document.createElement("button"); cancel.type = "button"; cancel.textContent = "Cancel";
  const proceed = document.createElement("button"); proceed.type = "button"; proceed.textContent = confirmLabel; if (danger) proceed.className = "confirm alarm";
  actions.append(cancel, proceed); box.append(actions);
  box.setAttribute("popover", "manual"); document.body.append(box); box.showPopover();
  const rect = anchor?.getBoundingClientRect?.() || { left: 8, right: 240, bottom: 8 };
  box.style.position = "fixed"; box.style.margin = "0"; box.style.left = `${Math.max(8, Math.min(rect.left, innerWidth - 240))}px`; box.style.top = `${Math.max(8, Math.min(rect.bottom + 4, innerHeight - box.offsetHeight - 8))}px`;
  return { box, field, cancel, proceed };
}

function ask(options) {
  return new Promise((resolve) => {
    const view = questionBox(options); let settled = false;
    const finish = (value) => { if (settled) return; settled = true; activeQuestion = null; view.box.remove(); resolve(value); };
    const controller = registerMenu(view.box, { anchor: options.anchor || document.activeElement, onClose: () => finish(options.input ? null : false) });
    activeQuestion = { cancel: () => finish(options.input ? null : false) };
    view.cancel.onclick = () => finish(options.input ? null : false);
    view.proceed.onclick = () => finish(options.input ? view.field.value.trim() || null : true);
    view.field?.addEventListener("keydown", (event) => { if (event.key === "Enter") { event.preventDefault(); view.proceed.click(); } });
    controller.open(); (view.field || view.cancel).focus(); if (view.field) view.field.select();
  });
}

export const requestConfirmation = (message, options = {}) => ask({ ...options, message });
export const requestText = (label, value = "", options = {}) => ask({ ...options, message: label, input: { label, value }, confirmLabel: options.confirmLabel || "Save" });
