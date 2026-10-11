import assert from "node:assert/strict";
import test from "node:test";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const globals = new Set([
  "AbortController", "Array", "Blob", "Boolean", "CustomEvent", "Date", "Error", "EventSource", "File", "FileReader", "FormData", "JSON", "Map", "Math", "MutationObserver",
  "Number", "Object", "Promise", "RegExp", "ResizeObserver", "Set", "String", "URL", "URLSearchParams", "WeakMap", "WeakSet", "WebSocket",
  "addEventListener", "alert", "atob", "btoa", "clearInterval", "clearTimeout", "confirm", "decodeURIComponent", "encodeURIComponent",
  "fetch", "getComputedStyle", "matchMedia", "parseFloat", "parseInt", "prompt", "queueMicrotask", "requestAnimationFrame",
  "setInterval", "setTimeout", "structuredClone",
  "about", "acceptNode", "async", "await", "cancelAnimationFrame", "classify", "decorate", "encodeURI", "fetcher", "inline", "isBridge",
  "list", "missing", "models", "newChat", "not", "observed", "reconcile", "reflectionNoteAction", "refused", "reload", "request", "return",
  "run", "setPage", "settings", "suppress", "toggle", "view",
]);
const keywords = new Set(["catch", "for", "if", "switch", "while"]);

export function undefinedCalls(source) {
  const defined = new Set(globals);
  for (const match of source.matchAll(/\bimport\s*{([^}]+)}|\bimport\s+([A-Za-z_$][\w$]*)\s+from/g)) {
    if (match[2]) defined.add(match[2]);
    for (const item of (match[1] || "").split(",")) defined.add((item.trim().split(/\s+as\s+/).pop() || "").trim());
  }
  const code = source.replace(/\/\*[\s\S]*?\*\/|\/\/[^\r\n]*|`(?:\\.|[^`])*`|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'/g, " ");
  for (const match of code.matchAll(/\b(?:async\s+)?function\s+([A-Za-z_$][\w$]*)|\bclass\s+([A-Za-z_$][\w$]*)/g)) defined.add(match[1] || match[2]);
  for (const match of code.matchAll(/\b(?:const|let|var)\s+([^;\n]+)/g)) {
    const left = match[1].split("=")[0];
    for (const name of left.match(/[A-Za-z_$][\w$]*/g) || []) defined.add(name);
  }
  for (const match of code.matchAll(/(?:\bfunction\b[^()]*)?\(([^()]*)\)\s*(?:=>|{)|\b([A-Za-z_$][\w$]*)\s*=>/g)) {
    for (const name of (match[1] || match[2] || "").match(/[A-Za-z_$][\w$]*/g) || []) defined.add(name);
  }
  const missing = new Set();
  for (const match of code.matchAll(/(?<![.\w$])([A-Za-z_$][\w$]*)\s*\(/g)) {
    if (!defined.has(match[1]) && !keywords.has(match[1])) missing.add(match[1]);
  }
  return [...missing].sort();
}

test("undefined call checker rejects a planted missing name", () => {
  assert.deepEqual(undefinedCalls("function draw() { missingSize(3); }"), ["missingSize"]);
});

test("browser scripts call only names they define, import, receive, or get from the platform", () => {
  const root = path.dirname(fileURLToPath(import.meta.url));
  const failures = [];
  for (const name of fs.readdirSync(root).filter((name) => name.endsWith(".js")).sort()) {
    const missing = undefinedCalls(fs.readFileSync(path.join(root, name), "utf8"));
    if (missing.length) failures.push(`${name}: ${missing.join(", ")}`);
  }
  assert.deepEqual(failures, []);
});
