import fs from "node:fs";
import path from "node:path";

// removeTreeWithinAllowedRoots is the only recursive removal Node scripts use,
// the counterpart of removal-guard.ps1. It refuses a target outside its explicit
// allow-list or a volume root, unlinks every junction and symbolic link inside
// the tree without following it, and only then removes the tree. v0.64.0/W1: a
// removal that descended through junctions emptied node_modules and .tools\go.
export function assertRemovalWithinAllowedRoots(target, allowedRoots, purpose = "removal") {
  if (!target || !Array.isArray(allowedRoots) || !allowedRoots.some(Boolean)) throw new Error(`Refusing ${purpose} without a path and explicit allowed removal roots.`);
  const full = trim(path.resolve(target));
  if (trim(path.parse(full).root).toLowerCase() === full.toLowerCase()) throw new Error(`Refusing ${purpose} of a volume root: ${full}`);
  for (const allowed of allowedRoots) {
    if (!allowed) continue;
    const root = trim(path.resolve(allowed));
    const a = full.toLowerCase();
    const b = root.toLowerCase();
    if (a === b || a.startsWith(b + path.sep)) return full;
  }
  throw new Error(`Refusing ${purpose} outside allowed removal roots: ${full}`);
}

export function removeTreeWithinAllowedRoots(target, allowedRoots, purpose = "removal") {
  const full = assertRemovalWithinAllowedRoots(target, allowedRoots, purpose);
  let stat;
  try { stat = fs.lstatSync(full); } catch (error) { if (error.code === "ENOENT") return; throw error; }
  if (stat.isSymbolicLink()) { fs.unlinkSync(full); return; }
  if (stat.isDirectory()) unlinkLinksWithin(full);
  fs.rmSync(full, { recursive: true, force: true });
}

function unlinkLinksWithin(directory) {
  const pending = [directory];
  while (pending.length) {
    const current = pending.pop();
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const child = path.join(current, entry.name);
      if (entry.isSymbolicLink()) fs.unlinkSync(child);
      else if (entry.isDirectory()) pending.push(child);
    }
  }
}

function trim(value) {
  return value.length > path.parse(value).root.length ? value.replace(/[\/]+$/, "") : value;
}
