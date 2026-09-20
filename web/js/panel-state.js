export function createPanelState(scope, storage = safeStorage()) {
  const states = new Map();
  return {
    view(id) {
      if (!states.has(id)) {
        states.set(id, {
          expanded: new Set(read(storage, key(scope, id))),
          scroll: 0,
          follow: true,
          toggle(value) {
            this.expanded.has(value) ? this.expanded.delete(value) : this.expanded.add(value);
            write(storage, key(scope, id), [...this.expanded]);
          },
        });
      }
      return states.get(id);
    },
  };
}

function safeStorage() {
  try {
    return globalThis.sessionStorage || null;
  } catch {
    return null;
  }
}

// Item 2gk: renamed with the page that is gone. A key that changes forgets
// which rows were open once, in this browser, and that is the whole cost.
function key(scope, id) {
  return `agentb.panel.${scope}.${id}.expanded`;
}

function read(storage, name) {
  if (!storage) return [];
  try {
    const value = JSON.parse(storage.getItem(name) || "[]");
    return Array.isArray(value) ? value.filter((item) => typeof item === "string") : [];
  } catch {
    return [];
  }
}

function write(storage, name, value) {
  if (!storage) return;
  try {
    storage.setItem(name, JSON.stringify(value));
  } catch {
    // Expansion remains usable in memory when browser storage is unavailable.
  }
}
