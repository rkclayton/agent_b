import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { assertRemovalWithinAllowedRoots, removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

function disposable() {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "agentb-removal-guard-"));
  const target = path.join(root, "target");
  fs.mkdirSync(target);
  fs.writeFileSync(path.join(target, "keep.txt"), "keep");
  return { root, target };
}

test("a removal outside the allow-list or of a volume root is refused and removes nothing", () => {
  const { root, target } = disposable();
  try {
    assert.throws(() => removeTreeWithinAllowedRoots(target, [path.join(root, "other")], "test"), /Refusing test outside allowed removal roots/);
    assert.throws(() => removeTreeWithinAllowedRoots(target, [], "test"), /without a path and explicit allowed removal roots/);
    assert.throws(() => assertRemovalWithinAllowedRoots(path.parse(root).root, [path.parse(root).root], "test"), /volume root/);
    assert.throws(() => removeTreeWithinAllowedRoots(`${target}-sibling`, [target], "test"), /outside allowed removal roots/);
    assert.ok(fs.existsSync(path.join(target, "keep.txt")));
  } finally {
    removeTreeWithinAllowedRoots(root, [root], "test cleanup");
  }
});

test("a junction inside a removed tree is unlinked and its target survives", () => {
  const { root, target } = disposable();
  try {
    const tree = path.join(root, "tree", "a");
    fs.mkdirSync(tree, { recursive: true });
    fs.symlinkSync(target, path.join(tree, "node_modules"), "junction");
    removeTreeWithinAllowedRoots(path.join(root, "tree"), [path.join(root, "tree")], "test");
    assert.equal(fs.existsSync(path.join(root, "tree")), false);
    assert.ok(fs.existsSync(path.join(target, "keep.txt")));
    fs.symlinkSync(target, path.join(root, "direct"), "junction");
    removeTreeWithinAllowedRoots(path.join(root, "direct"), [path.join(root, "direct")], "test");
    assert.ok(fs.existsSync(path.join(target, "keep.txt")));
  } finally {
    removeTreeWithinAllowedRoots(root, [root], "test cleanup");
  }
});

// Every deletion path in the repository's scripts goes through a guard: a raw
// recursive removal or a direct `git worktree remove` anywhere else fails here.
test("no script removes a tree or a worktree except through the removal guard", () => {
  const files = execFileSync("git", ["ls-files", "scripts", "tests", "cmd"], { cwd: repository, encoding: "utf8" }).split("\n")
    .filter((name) => /\.(ps1|psm1|mjs|js|cmd|sh)$/i.test(name) && !/removal-guard(\.test)?\.(ps1|mjs)$|remove-worktree\.ps1$/.test(name));
  const offenders = [];
  for (const name of files) {
    const full = path.join(repository, name);
    if (!fs.existsSync(full)) continue;
    fs.readFileSync(full, "utf8").split(/\r?\n/).forEach((line, index) => {
      if (/^\s*(#|\/\/)/.test(line)) return;
      const powershellTree = /Remove-Item\b/i.test(line) && /-Recurse\b/i.test(line) && !/-LiteralPath\s+\(?\$\w*Registry\w*/i.test(line); // registry keys, not the filesystem
      const nodeTree = /\brmSync\s*\(/.test(line) && /recursive\s*:\s*true/.test(line);
      const worktree = /worktree['"]?\s*,?\s*['"]?remove\b/i.test(line);
      const shell = /\b(rm\s+-r|rmdir\s+\/s|rd\s+\/s)/i.test(line);
      if (powershellTree || nodeTree || worktree || shell) offenders.push(`${name}:${index + 1}: ${line.trim()}`);
    });
  }
  assert.deepEqual(offenders, []);
});
