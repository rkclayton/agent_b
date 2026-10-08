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
