import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2mh: what the switcher shows. Read from source, the convention the other
// shell contract tests follow — shell.js reaches for the DOM at import time.
const shell = fs.readFileSync(new URL("./shell.js", import.meta.url), "utf8");
const tokens = fs.readFileSync(new URL("../css/tokens.css", import.meta.url), "utf8");

test("each agent row names its connection and model", () => {
  const row = shell.slice(shell.indexOf("function renderConnectionMenu"), shell.indexOf("row.onclick"));
  assert.match(row, /for \(const agent of store\.config\.agents/);
  assert.match(row, /shell-agent-name/);
  assert.match(row, /shell-connection-model/);
  assert.match(row, /String\(connection\?\.model \|\| ""\)/);
});

test("a path is shown by its basename, with the whole string on hover", () => {
  // (d): llama.cpp models are full GGUF paths.
  const row = shell.slice(shell.indexOf("function renderConnectionMenu"), shell.indexOf("row.onclick"));
  assert.match(row, /split\(/, "the path is not reduced");
  assert.match(row, /title="\$\{escapeHTML\(named \? rawModel/, "the full string is not kept on hover");
});

test("an untested connection says so rather than naming a model that does not exist", () => {
  // (e): "model" is not a model.
  const row = shell.slice(shell.indexOf("function renderConnectionMenu"), shell.indexOf("row.onclick"));
  assert.match(row, /rawModel !== "model"/);
  assert.match(row, /not tested/);
});

test("the agent name is largest and the model is not ellipsised", () => {
  assert.match(tokens, /\.shell-connection-choice span\.shell-connection-model\{[^}]*text-overflow:clip/);
  assert.match(tokens, /\.shell-connection-choice \.shell-agent-name\{[^}]*font:13px\/18px/);
  assert.match(tokens, /\.shell-agent-route\{[^}]*font:11px\/16px|\.shell-connection-choice\{[^}]*font:11px\/16px/);
});
