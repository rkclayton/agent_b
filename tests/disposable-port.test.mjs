import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const root = new URL("../", import.meta.url);
const installer = await readFile(new URL("tests/test-installer.ps1", root), "utf8");

// Item 2gu (v1.2.5): a disposable root can never wear production's port, and a
// configuration that still names it fails its own preflight rather than
// colliding with the operator's running Agent_b.
test("a disposable root refuses production's port", () => {
  assert.match(installer, /\$script:ProductionPort = 8790/);
  // The free port is re-drawn rather than accepted if it is production's.
  assert.match(installer, /if \(\$port -ne \$script:ProductionPort\) \{ return \$port \}/);
  // And the preflight refuses a configuration that names it, with the reason.
  assert.match(installer, /function Assert-DisposableListen/);
  assert.match(installer, /names production's port \$script:ProductionPort; a disposable root must listen elsewhere/);
  assert.match(installer, /Assert-DisposableListen -Listen \$installedConfig\.listen/);
});

// The suite's inventory sees a new script before it is staged, not after. Three
// releases shipped a scenario the clean-archive case could not copy because it
// was untracked, and the suite said nothing.
test("the suite's inventory includes untracked, non-ignored files", () => {
  const matches = installer.match(/ls-files --others --exclude-standard/g) || [];
  assert.equal(matches.length, 2, "both tree copies must see untracked files");
});

// Every other disposable-root creator asserts the same thing in its own words.
test("every scenario runner refuses production's port", async () => {
  for (const name of [
    "tests/ui-harness.mjs",
  ]) {
    const source = await readFile(new URL(name, root), "utf8");
    assert.match(source, /8790/, name);
  }
});
