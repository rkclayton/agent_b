import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, mkdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

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
		"tests/chat-acceptance.mjs",
		"tests/chat-replay-acceptance.mjs",
		"tests/accounting-real-template-acceptance.mjs",
		"tests/comparative/run-suite.mjs",
  ]) {
    const source = await readFile(new URL(name, root), "utf8");
    assert.match(source, /8790/, name);
  }
});

test("installed launcher refuses a foreign HTTP responder", { skip: process.platform !== "win32" }, async () => {
	const rootDir = await mkdtemp(join(tmpdir(), "agentb-foreign-launcher-"));
	const app = join(rootDir, "Application", "Agent_b"), data = join(rootDir, "Data", "Agent_b");
	const server = createServer((_request, response) => response.end("foreign"));
	try {
		await mkdir(app, { recursive: true });
		await mkdir(join(data, "logs"), { recursive: true });
		await writeFile(join(app, "Agent_b.exe"), "not the listener");
		await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
		const port = server.address().port;
		assert.notEqual(port, 8790);
		const config = join(data, "harness.json");
		await writeFile(config, JSON.stringify({ listen: `127.0.0.1:${port}` }));
		const child = spawn("powershell.exe", ["-NoLogo", "-NoProfile", "-File", new URL("../scripts/launch-Agent_b.ps1", import.meta.url).pathname.slice(1), "-ApplicationDirectory", app, "-DataDirectory", data, "-ConfigPath", config, "-NoPause", "-NoBrowser", "-Detached"], { stdio: ["ignore", "pipe", "pipe"] });
		let output = "";
		child.stdout.on("data", (chunk) => output += chunk);
		child.stderr.on("data", (chunk) => output += chunk);
		const status = await new Promise((resolve, reject) => { child.once("error", reject); child.once("close", resolve); });
		assert.equal(status, 1, output);
		assert.match(output, /Foreign instance refused: PID \d+ owns port \d+ but does not match this install path and data root/);
	} finally {
		await new Promise((resolve) => server.close(resolve));
		removeTreeWithinAllowedRoots(rootDir, [tmpdir()], "foreign-launcher fixture cleanup");
	}
});
