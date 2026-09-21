// stage-candidate.mjs is the release's staging step (v0.69.0/W0): the tagged
// commit's clean archive is extracted to candidates\<tag>\ at the repository
// root, the candidate's own build-candidate.ps1 builds it, and only the three
// newest staged versions are kept. The report's install path is
// candidates\<tag>\install-Agent_b.cmd, read from the staged folder.
//
//   node scripts/stage-candidate.mjs --tag v0.69.0
//
// The worker's scratch for an order lives under %TEMP%\agentb-worker\<order>\:
//
//   node scripts/stage-candidate.mjs --scratch v0.69.0          (create, print)
//   node scripts/stage-candidate.mjs --purge-scratch v0.69.0    (remove it)
import { spawnSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "./removal-guard.mjs";

export const keepStaged = 3;
const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

function versionKey(name) {
  const match = /^v(\d+)\.(\d+)\.(\d+)$/.exec(name);
  return match ? match.slice(1).map(Number) : null;
}

// stagedToRemove names the staged version folders beyond the newest `keep`,
// by version order; anything not named like a version is never touched.
export function stagedToRemove(names, keep = keepStaged) {
  const versions = names.filter((name) => versionKey(name)).sort((left, right) => {
    const a = versionKey(left), b = versionKey(right);
    for (let index = 0; index < 3; index++) if (a[index] !== b[index]) return b[index] - a[index];
    return 0;
  });
  return versions.slice(keep);
}

const orderName = /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/;

export function workerScratchRoot(temp = os.tmpdir()) {
  return path.join(temp, "agentb-worker");
}

export function workerScratch(order, temp = os.tmpdir()) {
  if (!orderName.test(order || "")) throw new Error(`invalid order id for scratch: ${order}`);
  return path.join(workerScratchRoot(temp), order);
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, { stdio: "inherit", ...options });
  if (result.status !== 0) throw new Error(`${command} ${args.join(" ")} exited ${result.status}`);
  return result;
}

function stage(tag) {
  if (!versionKey(tag)) throw new Error(`--tag must look like vX.Y.Z: ${tag}`);
  const commit = spawnSync("git", ["-C", repoRoot, "rev-parse", `${tag}^{commit}`], { encoding: "utf8" });
  if (commit.status !== 0) throw new Error(`tag ${tag} does not resolve to a commit`);
  const sha = commit.stdout.trim();
  const candidates = path.join(repoRoot, "candidates");
  const target = path.join(candidates, tag);
  if (fs.existsSync(target)) throw new Error(`refusing to restage over ${target}`);
  fs.mkdirSync(target, { recursive: true });
  const tar = path.join(os.tmpdir(), `agentb-stage-${tag}-${process.pid}.tar`);
  try {
    run("git", ["-C", repoRoot, "archive", "--format=tar", "-o", tar, tag]);
    // Windows' own tar reads drive-letter paths; Git's GNU tar would not.
    const tarExe = process.platform === "win32" ? path.join(process.env.SystemRoot || "C:\\Windows", "System32", "tar.exe") : "tar";
    run(tarExe, ["-xf", tar, "-C", target]);
  } finally {
    fs.rmSync(tar, { force: true });
  }
  run("powershell.exe", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path.join(target, "scripts", "build-candidate.ps1"),
    "-SourceDirectory", target, "-Commit", sha, "-Dirty", "false", "-ExpectedTag", tag]);
  // Item 2gl (v1.2.0/W2): the candidate carries a setup executable. It is a
  // COPY of the build that was just verified against the manifest — the same
  // bytes under the name the operator double-clicks — so there is nothing
  // extra to build and nothing that can differ from what was checked.
  const built = path.join(target, "Agent_b.exe");
  const setup = path.join(target, "Agent_b-setup.exe");
  fs.copyFileSync(built, setup);
  const identical = fs.readFileSync(built).equals(fs.readFileSync(setup));
  if (!identical) throw new Error("Agent_b-setup.exe is not the verified build");
  const manifestPath = path.join(target, "candidate-final.json");
  const manifest = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
  manifest.setup_sha256 = manifest.exe_sha256;
  manifest.setup_bytes = fs.statSync(setup).size;
  fs.writeFileSync(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
  console.log(`SETUP: ${setup} (a copy of the verified Agent_b.exe)`);

  for (const name of stagedToRemove(fs.readdirSync(candidates))) {
    removeTreeWithinAllowedRoots(path.join(candidates, name), [candidates], "staged candidate rotation");
    console.log(`ROTATED: removed ${path.join(candidates, name)}`);
  }
  console.log(`STAGED: ${path.join(target, "install-Agent_b.cmd")}`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [flag, value] = process.argv.slice(2);
  try {
    if (flag === "--tag") stage(value);
    else if (flag === "--scratch") {
      const dir = workerScratch(value);
      fs.mkdirSync(dir, { recursive: true });
      console.log(dir);
    } else if (flag === "--purge-scratch") {
      const dir = workerScratch(value);
      if (fs.existsSync(dir)) removeTreeWithinAllowedRoots(dir, [workerScratchRoot()], "worker scratch purge");
      console.log(`PURGED: ${dir}`);
    } else throw new Error("usage: --tag vX.Y.Z | --scratch <order> | --purge-scratch <order>");
  } catch (error) {
    console.error(error.message);
    process.exit(1);
  }
}
