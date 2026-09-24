import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const settings = await readFile(new URL("./settings.js", import.meta.url), "utf8");
const security = await readFile(new URL("./settings-security.js", import.meta.url), "utf8");
const about = await readFile(new URL("./settings-about.js", import.meta.url), "utf8");

test("Settings exposes signature status but no signing controls or API", () => {
  const source = settings + security;
  assert.doesNotMatch(source, /\/api\/signing|Create certificate|Import certificate|Sign application|Verify signatures|signing-pfx|signing-password/);
  assert.match(about, /signatureWord/);
  assert.match(about, /"signed" : "unsigned"/);
});
