import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("./settings.js", import.meta.url), "utf8");

test("Security signing onboarding is gated and standard users see Verify only", () => {
  assert.match(source, /Create certificate/);
  assert.match(source, /Import certificate/);
  assert.match(source, /Sign application/);
  assert.match(source, /Verify signatures/);
  assert.match(source, /signingStatus\.can_manage/);
  assert.match(source, /Standard users can verify signatures but cannot create/);
  assert.match(source, /stable publisher identity and trusted local chain/);
});
