import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";

// Item 2pz: no prompt, string the model reads or string the page shows calls the person "the operator".
// Not words he reads: identifiers (operator_context, settings-operator-status), "chain operators",
// icon paths, reflection's pattern reading a model's old wording, the log label's match of a stored reason.
const root = path.resolve(import.meta.dirname, ".."), word = /(?<![\w-])[Oo]perator(?:'s)?(?![\w-])/;
const exempt = /\/static\/assets\/operator-|\(\?i\)\\b\(\?:the operator\||\(user\|operator\) request\$\//;
const literals = (file, source) => file.endsWith(".md") ? [source] : file.endsWith(".html") ? [...source.replace(/<!--[\s\S]*?-->/g, "").matchAll(/>([^<>]*)</g)].map((m) => m[1])
  : [...source.replace(/'(?:\\.[^'\n]{0,9}|[^'\\\n])'/g, file.endsWith(".go") ? "0" : "$&").replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:"'`\\])\/\/[^\n]*/g, "$1")
    .matchAll(/(?<!log\.Printf?\()(?:"((?:[^"\\\n]|\\.)*)"|'((?:[^'\\\n]|\\.)*)'|`([^`]*)`)/g)].map((m) => m[1] ?? m[2] ?? m[3]);

test("no prompt, model-facing string or shipped UI string calls him the operator (2pz)", () => {
  const offenders = ["prompts", "web", "internal", "cmd"].flatMap((dir) => fs.readdirSync(path.join(root, dir), { recursive: true }).map((name) => path.join(dir, name)))
    .filter((file) => /\.(md|js|html|go)$/.test(file) && !/(_test\.go|\.test\.mjs|\.spec\.mjs)$/.test(file) && !(file.endsWith(".md") && !file.startsWith("prompts")))
    .flatMap((file) => literals(file, fs.readFileSync(path.join(root, file), "utf8")).filter((text) => /\s/.test(text.trim()) && word.test(text) && !exempt.test(text)).map((text) => `${file}: ${text.slice(0, 100)}`));
  assert.deepEqual(offenders, [], `${offenders.length} string(s) say operator`);
});
