import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2mh: what the switcher shows. Read from source, the convention the other
// shell contract tests follow — shell.js reaches for the DOM at import time.
const shell = fs.readFileSync(new URL("./shell.js", import.meta.url), "utf8");
const tokens = fs.readFileSync(new URL("../css/tokens.css", import.meta.url), "utf8");

test("the switcher row names the model, not only the label", () => {
  // (b): at any width, "server-2" identifies nothing. The model is what the
  // operator is choosing between.
  const row = shell.slice(shell.indexOf("Item 2mh (b)"), shell.indexOf("row.onclick"));
  assert.match(row, /shell-connection-model/);
  assert.match(row, /connection\.model/);
});

test("a path is shown by its basename, with the whole string on hover", () => {
  // (d): llama.cpp models are full GGUF paths.
  const row = shell.slice(shell.indexOf("Item 2mh (b)"), shell.indexOf("row.onclick"));
  assert.match(row, /split\(/, "the path is not reduced");
  assert.match(row, /title="\$\{escapeHTML\(named \? rawModel/, "the full string is not kept on hover");
});

test("an untested connection says so rather than naming a model that does not exist", () => {
  // (e): "model" is not a model.
  const row = shell.slice(shell.indexOf("Item 2mh (b)"), shell.indexOf("row.onclick"));
  assert.match(row, /rawModel !== "model"/);
  assert.match(row, /not tested/);
});

test("the model name is not ellipsised and the column has room", () => {
  // (c): the menu is 680px and the name column was flooring at 90.
  // Item 2mh (c): the rule has to BEAT `.shell-connection-choice span`, which is a
  // class and an element. Measured in the running app at rel-1.31.0: the bare class
  // lost and the browser computed ellipsis on the model name.
  assert.match(tokens, /\.shell-connection-choice span\.shell-connection-model\{[^}]*text-overflow:clip/);
  const grid = /\.shell-connection-choice\{[^}]*grid-template-columns:([^;}]+)/.exec(tokens);
  assert.ok(grid, "the row's grid is gone");
  assert.match(grid[1], /minmax\(180px/, "the model column did not get room");
});
