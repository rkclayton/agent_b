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
    if (isLink(ancestor, stat)) throw new Error(`Refusing ${purpose} beneath a junction or link: ${ancestor}`);
  }
  return full;
}

export function removeTreeWithinAllowedRoots(target, allowedRoots, purpose = "removal") {
  const full = assertRemovalWithinAllowedRoots(target, allowedRoots, purpose);
  removeEntryWithoutFollowing(full, full);
}

// v0.65.0/W15 cold review: Node reports a junction to a volume GUID path
// (\??\Volume{…}\) as a plain directory, so lstat's isSymbolicLink is not
// enough. An entry is a link when it is a symbolic link or when its real path is
// not its real parent joined with its own name.
function isLink(entry, stat) {
  if (stat.isSymbolicLink()) return true;
  if (!stat.isDirectory()) return false;
  try {
    const real = fs.realpathSync.native(entry).toLowerCase();
    const expected = path.join(fs.realpathSync.native(path.dirname(entry)), path.basename(entry)).toLowerCase();
    return real !== expected;
  } catch {
    return true;
  }
}

// Every directory between an entry and the removal root must still be a real
// directory; one swapped for a junction during the walk stops it.
function assertNoLinkAbove(entry, root) {
  for (let ancestor = path.dirname(entry); ancestor.length >= root.length; ancestor = path.dirname(ancestor)) {
    let stat;
    try { stat = fs.lstatSync(ancestor); } catch { throw new Error(`Refusing removal: ${ancestor} went missing while ${root} was being removed`); }
    if (isLink(ancestor, stat)) throw new Error(`Refusing removal: ${ancestor} became a junction or link while ${root} was being removed`);
    if (ancestor.toLowerCase() === root.toLowerCase()) return;
    if (path.dirname(ancestor) === ancestor) return;
  }
}

function removeEntryWithoutFollowing(entry, root) {
  if (entry !== root) assertNoLinkAbove(entry, root);
  let stat;
  try { stat = fs.lstatSync(entry); } catch (error) { if (error.code === "ENOENT") return; throw error; }
  if (isLink(entry, stat)) { unlinkLink(entry); return; }
  if (!stat.isDirectory()) { fs.rmSync(entry, { force: true }); return; }
  for (const name of fs.readdirSync(entry)) removeEntryWithoutFollowing(path.join(entry, name), root);
  // Read again: if the directory became a link while its children were removed,
  // unlink the link rather than remove anything through it.
  if (entry !== root) assertNoLinkAbove(entry, root);
  const again = fs.lstatSync(entry);
  if (isLink(entry, again)) unlinkLink(entry);
  else fs.rmdirSync(entry);
}

function unlinkLink(entry) {
  try { fs.unlinkSync(entry); } catch (error) { if (error.code === "EPERM" || error.code === "EISDIR") fs.rmdirSync(entry); else throw error; }
}

function trim(value) {
  return value.length > path.parse(value).root.length ? value.replace(/[\/]+$/, "") : value;
}
