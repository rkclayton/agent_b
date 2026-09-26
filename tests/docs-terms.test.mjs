import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

// Item 2jm (d): a docs check that fails on terms the product no longer uses, so
// the next rename cannot leave the documentation behind.
//
// The hard part is not finding a word. It is telling a STALE CLAIM from correct
// prose that happens to contain the same word, which rel-1.16.0/W0 found twice:
//
//   - `-Console` is a real launcher flag for an attached visible console. The
//     dissolved SURFACE is the stale thing (item 2gk), not the string.
//   - `%ProgramFiles%` is where PowerShell 7 lives, and is where an all-users
//     install puts the application. Only "%ProgramFiles% as the per-user default
//     install root" is false.
//
// So each rule below matches the CLAIM, not the term, and every rule carries a
// negative control: a line that must trip it. A rule with no negative control is
// a rule nobody has tested.

const documents = [
  "README.md",
  "docs/REFERENCE.md",
  "docs/HARDENING.md",
  "docs/OPERATOR-FILES.md",
  "docs/SCREENSHOT-GATE.md",
  "SECURITY.md",
  "INTERFACES.md",
  "web/DESIGN.md",
  "AGENTS.md",
  "CLAUDE.md",
  "tests/README.md",
];

const rules = [
  {
    name: "Console as a surface",
    why: "item 2gk dissolved Console into Settings -> Agents and Activity",
    // A surface is written as a destination or a subject: "on Console",
    // "for Console", "Console shows", or listed beside the other pages.
    pattern: /(?:\bon Console\b|\bfor Console\b|\bConsole (?:shows|displays|owns|holds|timeline)\b|\bChat,\s*Console\b|\bConsole,\s*Plan\b|\*\*Console\*\*)/,
    trips: "The **Console** shows live activity, and Chat, Console, Plan and Settings are the pages.",
    allows: "The normal hidden launcher wrapper owns the process; pass `-Console` for an attached visible console window.",
  },
  {
    name: "servers[] as the configuration key",
    why: "connections replaced servers[] (item 2jc)",
    pattern: /\bservers\s*\[\s*\]/,
    trips: "Set the endpoint in `servers[]`.",
    allows: "Connections target an OpenAI-compatible server at `base_url`.",
  },
  {
    name: "%ProgramFiles% as the default install root",
    why: "the default install is per-user under %LocalAppData%\\Programs since item 2ja; %ProgramFiles% is the all-users root and the PowerShell 7 location",
    pattern: /%ProgramFiles%[^.\n]{0,80}\b(?:default|normal)\b|\b(?:default|normal)(?:ly)?[^.\n]{0,60}%ProgramFiles%/i,
    trips: "The application is installed by default into %ProgramFiles%\\Agent_b.",
    allows: "PowerShell 7 when it is installed machine-wide (`%ProgramFiles%\\PowerShell\\7\\pwsh.exe`), and an all-users install puts the application under `%ProgramFiles%\\Agent_b`.",
  },
  {
    name: "one UAC prompt on a per-user install",
    why: "the per-user install needs no UAC at all since item 2ja; the single approval is the service identity at first launch",
    pattern: /\bone UAC prompt\b/i,
    trips: "Installing takes one UAC prompt.",
    allows: "The normal per-user install needs no UAC prompt or console window.",
  },
  {
    name: "a roles screen in setup",
    why: "the roles screen was removed by items 2hm and 2iq; setup connects b then c",
    pattern: /\broles screen\b/i,
    trips: "Assign the connection on the roles screen.",
    allows: "Settings -> Agents gives each role its model.",
  },
  {
    name: "a profile as an endpoint",
    why: "a profile is the operator concept (item 2jd); an endpoint is a connection (item 2jc)",
    pattern: /\bprofile(?:'s)?\s+base_url\b|\bprofile\s+endpoint\b|\bendpoint\s+profile\b/i,
    trips: "Set the profile base_url for each endpoint profile.",
    allows: "Profiles keep separate chats, memory and logs; connections carry base_url.",
  },
];

// Every rule must actually catch what it claims to catch, and must not catch the
// correct prose beside it. This is the negative control the item asks for, and it
// runs on every suite, not only when someone remembers.
test("each stale-term rule trips on its own reintroduction and spares correct prose", () => {
  for (const rule of rules) {
    assert.match(rule.trips, rule.pattern, `the rule "${rule.name}" does not catch its own negative control`);
    assert.doesNotMatch(rule.allows, rule.pattern, `the rule "${rule.name}" flags correct prose`);
  }
});

const sources = Object.fromEntries(await Promise.all(documents.map(async (name) => {
  try {
    return [name, (await readFile(new URL(`../${name}`, import.meta.url), "utf8")).replace(/\r\n/g, "\n")];
  } catch {
    return [name, null];
  }
})));

test("every shipped document exists", () => {
  const missing = documents.filter((name) => sources[name] === null);
  assert.deepEqual(missing, [], `shipped documents are missing: ${missing.join(", ")}`);
});

test("no shipped document makes a claim the product no longer supports", () => {
  const found = [];
  for (const [name, text] of Object.entries(sources)) {
    if (text === null) continue;
    text.split("\n").forEach((line, index) => {
      for (const rule of rules) {
        if (rule.pattern.test(line)) found.push(`${name}:${index + 1} ${rule.name} -- ${rule.why}\n    ${line.trim().slice(0, 140)}`);
      }
    });
  }
  assert.deepEqual(found, [], `stale claims:\n${found.join("\n")}`);
});

// (c): the handoff note is gone and nothing points at it.
test("no shipped document references the deleted handoff note", () => {
  const referrers = Object.entries(sources)
    .filter(([, text]) => text !== null && text.includes("2g-attachment-ingest-handoff"))
    .map(([name]) => name);
  assert.deepEqual(referrers, []);
});
