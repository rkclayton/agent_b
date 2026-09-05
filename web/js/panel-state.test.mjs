import assert from "node:assert/strict";
import test from "node:test";

import { createPanelState } from "./panel-state.js";

function memoryStorage() {
  const values = new Map();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => values.set(key, String(value)),
  };
}

for (const scope of ["timeline", "state"]) {
  test(`${scope} expansion survives event renders and refresh`, () => {
    const storage = memoryStorage();
    const live = createPanelState(scope, storage);
    live.view("main").toggle("row-1");
    for (let event = 0; event < 50; event++)
      assert.equal(live.view("main").expanded.has("row-1"), true);

    const refreshed = createPanelState(scope, storage);
    assert.equal(refreshed.view("main").expanded.has("row-1"), true);
  });
}

test("timeline group and child expansion both survive rerenders and refresh", () => {
  const storage = memoryStorage();
  const live = createPanelState("timeline", storage);
  const view = live.view("main");
  assert.equal(view.expanded.has("tool-group:read-1"), false);
  view.toggle("tool-group:read-1");
  view.toggle("tool:read-2");
  for (let event = 0; event < 50; event++) {
    assert.equal(live.view("main").expanded.has("tool-group:read-1"), true);
    assert.equal(live.view("main").expanded.has("tool:read-2"), true);
  }

  const refreshed = createPanelState("timeline", storage).view("main");
  assert.equal(refreshed.expanded.has("tool-group:read-1"), true);
  assert.equal(refreshed.expanded.has("tool:read-2"), true);
});
