import assert from "node:assert/strict";
import fs from "node:fs";
import test from "node:test";

// Item 2ly (c): a settings page that follows the profile says so, and the
// classification it says it from lives in Go. Two lists in two languages agree
// only while something checks them.
//
// This does not compare the lists directly -- they are different shapes, one of
// SETTING KEYS and one of NAV SECTIONS. It DERIVES the sections from the keys, by
// reading which page each nav section renders and which keys that page edits. The
// first version of the marking named two content ids that are not nav sections at
// all and missed two that are; a test that only checked the names it was given
// would have passed.
const read = (relative) => fs.readFileSync(new URL(relative, import.meta.url), "utf8");

const settingsSource = read("./settings.js");
const scopesSource = read("../../internal/config/scopes.go");

// Two sections edit a per-profile key WITHOUT naming it in a settings path, so
// no amount of source reading will find them. They are listed here, with the key
// each one owns, and the key is still checked against Go -- so if either stops
// being per-profile there, this fails rather than going quietly stale.
const indirect = {
  // The Agents page adopts a panel rather than rendering a settings page, and the
  // panel writes config.agents (each agent's connection assignment).
  agents: "agents",
  // The Notifications page saves a DPAPI credential, and the profile's
  // notifications.discord_credential is the NAME it saves under -- so which
  // webhook the page is editing follows the profile even though the page never
  // writes a "notifications." path.
  notifications: "notifications",
};

// settings.js reaches for EventSource at import time, so the list is read out of
// its source rather than imported -- the convention the other settings contract
// tests follow.
const marked = new Set(
  [...(/export const perProfileSections = new Set\(\[([^\]]*)\]\)/.exec(settingsSource)?.[1] ?? "")
    .matchAll(/"([a-z_]+)"/g)].map((match) => match[1]),
);

function profileScopedKeys() {
  const body = scopesSource.slice(scopesSource.indexOf("var scopes = map[string]Scope{"));
  const keys = new Set();
  for (const line of body.split(/\r?\n/)) {
    if (line.startsWith("}")) break;
    const found = /^\s*"([a-z_]+)":\s*ScopeProfile\b/.exec(line);
    if (found) keys.add(found[1]);
  }
  return keys;
}

function navSections() {
  const block = /const sectionLabels = \[([\s\S]*?)\];/.exec(settingsSource)?.[1] ?? "";
  return [...block.matchAll(/\["([a-z_]+)",/g)].map((match) => match[1]);
}

// Which modules a section's page pulls in, one import level deep -- enough to
// reach the Run and Context pages that the Chats page composes into itself.
function sourcesForSection(section) {
  const line = new RegExp(`^\\s*${section}: \\(\\) =>(.*)$`, "m").exec(settingsSource)?.[1] ?? "";
  const imports = new Map(
    [...settingsSource.matchAll(/import \{([^}]*)\} from "\.\/([^"]+)"/g)].flatMap(([, names, file]) =>
      names.split(",").map((name) => [name.trim(), file]),
    ),
  );
  const files = new Set();
  for (const [, name] of line.matchAll(/\b(render[A-Za-z]+)\(/g)) {
    const file = imports.get(name);
    if (file) files.add(file);
  }
  const sources = [];
  for (const file of files) {
    const body = read(`./${file}`);
    sources.push(body);
    for (const [, nested] of body.matchAll(/import \{[^}]*\} from "\.\/(settings-[^"]+)"/g)) {
      sources.push(read(`./${nested}`));
    }
  }
  return sources.join("\n");
}

function sectionsThatEditAProfileKey() {
  const keys = profileScopedKeys();
  const sections = new Set(Object.keys(indirect));
  for (const section of navSections()) {
    const source = sourcesForSection(section);
    for (const key of keys) {
      if (new RegExp(`"${key}\\.`).test(source) || new RegExp(`config\\.${key}\\b`).test(source)) {
        sections.add(section);
        break;
      }
    }
  }
  return sections;
}

test("the per-profile keys in Go are the sections the browser marks", () => {
  const derived = sectionsThatEditAProfileKey();
  assert.ok(derived.size > 1, "no page was found to edit a per-profile key; the derivation is wrong, not the data");
  assert.deepEqual(
    [...marked].sort(),
    [...derived].sort(),
    "the marked sections and the sections that edit a per-profile setting disagree",
  );
});

test("every section claimed to edit a key indirectly still owns a per-profile key", () => {
  const keys = profileScopedKeys();
  for (const [section, key] of Object.entries(indirect)) {
    assert.ok(keys.has(key), `"${section}" is marked for "${key}" and internal/config/scopes.go no longer scopes it to the profile`);
  }
});

test("every marked section is a section an operator can actually open", () => {
  // The defect this catches: "sessions" and "memory" are entries of the content
  // map and NOT of sectionLabels, so they can never be the active section. A
  // scope note on a page nobody can reach is not a statement, it is a comment.
  const nav = new Set(navSections());
  for (const section of marked) {
    assert.ok(nav.has(section), `"${section}" is marked per-profile and is not a nav section`);
  }
});
