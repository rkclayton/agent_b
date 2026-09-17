import fs from "node:fs";
import path from "node:path";

// removeTreeWithinAllowedRoots is the only recursive removal Node scripts use,
// the counterpart of removal-guard.ps1. It refuses a target outside its explicit
// allow-list, a volume root, an allowed root that is the target itself, and a
// target beneath a junction or symbolic link. It then removes the tree bottom-up,
// reading each entry again as it acts on it: a link is unlinked and never
// entered, and only a real directory is descended into. v0.64.0/W1: a removal
// that descended through junctions emptied node_modules and .tools\go.
export function assertRemovalWithinAllowedRoots(target, allowedRoots, purpose = "removal") {
  if (!target || !Array.isArray(allowedRoots) || !allowedRoots.some(Boolean)) throw new Error(`Refusing ${purpose} without a path and explicit allowed removal roots.`);
  const full = trim(path.resolve(target));
  if (trim(path.parse(full).root).toLowerCase() === full.toLowerCase()) throw new Error(`Refusing ${purpose} of a volume root: ${full}`);
  let contained = false;
  for (const allowed of allowedRoots) {
    if (!allowed) continue;
    const root = trim(path.resolve(allowed));
    const a = full.toLowerCase();
    const b = root.toLowerCase();
    // v0.65.0/W9: an allowed root is the container a removal stays inside.
    if (a === b) throw new Error(`Refusing ${purpose}: an allowed removal root must contain the target, not be it: ${full}`);
    if (a.startsWith(b + path.sep)) contained = true;
  }
  if (!contained) throw new Error(`Refusing ${purpose} outside allowed removal roots: ${full}`);
  // v0.65.0/W9: a junction or link in any existing ancestor redirects the whole
  // removal into its target.
  const volume = trim(path.parse(full).root).toLowerCase();
  for (let ancestor = path.dirname(full); trim(ancestor).toLowerCase() !== volume; ancestor = path.dirname(ancestor)) {
    let stat;
    try { stat = fs.lstatSync(ancestor); } catch (error) { if (error.code === "ENOENT") continue; throw error; }
    if (stat.isSymbolicLink()) throw new Error(`Refusing ${purpose} beneath a junction or link: ${ancestor}`);
  }
  return full;
}

export function removeTreeWithinAllowedRoots(target, allowedRoots, purpose = "removal") {
  const full = assertRemovalWithinAllowedRoots(target, allowedRoots, purpose);
  removeEntryWithoutFollowing(full);
}

function removeEntryWithoutFollowing(entry) {
  let stat;
  try { stat = fs.lstatSync(entry); } catch (error) { if (error.code === "ENOENT") return; throw error; }
  if (stat.isSymbolicLink()) { unlinkLink(entry); return; }
  if (!stat.isDirectory()) { fs.rmSync(entry, { force: true }); return; }
  for (const name of fs.readdirSync(entry)) removeEntryWithoutFollowing(path.join(entry, name));
  // Read again: if the directory became a link while its children were removed,
  // unlink the link rather than remove anything through it.
  const again = fs.lstatSync(entry);
  if (again.isSymbolicLink()) unlinkLink(entry);
  else fs.rmdirSync(entry);
}

function unlinkLink(entry) {
  try { fs.unlinkSync(entry); } catch (error) { if (error.code === "EPERM" || error.code === "EISDIR") fs.rmdirSync(entry); else throw error; }
}

function trim(value) {
  return value.length > path.parse(value).root.length ? value.replace(/[\/]+$/, "") : value;
}
