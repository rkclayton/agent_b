import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

const source = readFileSync(new URL("./chat-acceptance.mjs", import.meta.url), "utf8");
const journeys = JSON.parse(readFileSync(new URL("./acceptance/journeys.json", import.meta.url), "utf8"));
const runner = readFileSync(new URL("./test-chat-journeys.ps1", import.meta.url), "utf8");
const builder = readFileSync(new URL("../tools/build-candidate.ps1", import.meta.url), "utf8");
const signer = readFileSync(new URL("../tools/sign-test-candidate.ps1", import.meta.url), "utf8");

test("every former packaged checkpoint belongs to exactly one named journey 2vm", () => {
  const former = [...source.matchAll(/record\("([^"]+)"\)/g)].map((match) => match[1]).filter((name) => !name.startsWith("journey-"));
  const expected = [...new Set(former)].sort();
  const mapped = journeys.flatMap(({ steps }) => steps);
  assert.equal(journeys.length, 13);
  assert.deepEqual([...new Set(mapped)].sort(), expected);
  assert.equal(mapped.length, new Set(mapped).size, "a former checkpoint is assigned more than once");
  assert.deepEqual(journeys.find(({ name }) => name === "undo")?.steps, []);
});

test("the journey runner builds once, isolates profiles, and reruns one failure once 2vm", () => {
  assert.match(runner, /\$manifest\s*=\s*@\(\$decodedManifest\s*\|\s*ForEach-Object\s*\{\s*\$_\s*\}\)/, "Windows PowerShell must enumerate the decoded journey array");
  assert.ok(runner.indexOf("build-candidate.ps1") < runner.indexOf("foreach ($name in $Journeys)"));
  assert.match(runner, /for \(\$attempt = 1; \$attempt -le 2; \$attempt\+\+\)/);
  assert.match(runner, /test-chat-acceptance\.ps1.*-SkipBuild/s);
  assert.match(runner, /-StandardOutput \$stdout -StandardError \$stderr/);
  assert.match(runner, /\$child\.WaitForExit\(\)\s*\$child\.Refresh\(\)[\s\S]*\$exitCode\s*=\s*\[int\]\$child\.ExitCode/);
  assert.match(runner, /native terminal missing: JOURNEY PASS/);
  assert.match(runner, /'flaky'/);
  assert.match(runner, /'failed-required'/);
  assert.match(runner, /'known-failure'/);
  assert.match(runner, /test_signature_timestamped=\$false/);
});

test("packaged acceptance uses a trusted untimestamped test copy without a timestamp request 2vm", () => {
  assert.match(runner, /-TestSignatureWithoutTimestamp/);
  assert.match(builder, /-NoTimestamp:\$TestSignatureWithoutTimestamp/g);
  // Item 2x4 (c): the untimestamped copy returns before the timestamped call and its one retry.
  assert.match(signer, /if \(\$NoTimestamp\) \{ return Set-AuthenticodeSignature[^\n]+-HashAlgorithm SHA256 \}\n[\s\S]*Set-AuthenticodeSignature[^\n]+-TimestampServer \$TimestampUrl/);
  assert.doesNotMatch(signer.match(/if \(\$NoTimestamp\) \{ return [^\n]+/)[0], /TimestampServer/);
});
