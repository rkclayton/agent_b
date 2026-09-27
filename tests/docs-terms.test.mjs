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
  "docs/REPOSITORIES.md",
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
  {
    // Item 2lr: the decision is only worth writing down if it cannot quietly
    // rot. The claim that would rot is a telemetry RECEIVER in this repository;
    // the sender is here and the word alone proves nothing.
    name: "a telemetry receiver in this repository",
    why: "item 2lr: the receiver is the VPS; this repository ships the sender and the off switch only",
    pattern: /\b(?:receiver|receiving endpoint)\b[^.\n]{0,60}\b(?:in|inside|on|within) (?:this repository|the harness|Agent_b)\b|\bAgent_b (?:hosts|runs|serves|provides) (?:a|the) telemetry receiver\b/i,
    trips: "Agent_b runs a telemetry receiver, and the receiver in this repository stores each batch.",
    allows: "This repository keeps only the sender and the off switch; the receiver is the VPS.",
  },
  {
    // Item 2lz: the decision is that the installer does NOT touch PATH, and the
    // docs carry it. What would rot is a claim that it does -- somebody adding
    // the entry later and leaving the reasoning, or the reverse. This matches
    // the CLAIM, so "add the directory to your PATH" (an instruction to the
    // reader) still passes while "the installer adds it" does not.
    name: "the installer putting agentb on PATH",
    why: "item 2lz decided against it: editing shared, length-limited machine state on every deployment pass breaks working machines",
    pattern: /\b(?:the installer|setup)\b(?:(?!\b(?:does not|doesn't|never|declined|will not|won't)\b)[^.\n]){0,40}\b(?:adds|puts|places|writes)\b[^.\n]{0,30}\bPATH\b/i,
    trips: "The installer adds agentb to your PATH, so agentb is on your PATH after setup.",
    allows: "The installer does not put it on your PATH; add the directory once if you want it.",
  },
  {
    // A documented path that lost its backslashes is a path nobody can use, and
    // v1.19.0 shipped exactly that: "%LocalAppData%ProgramsAgent_bagentb.exe".
    // It looked fine in review because the words were all there.
    name: "a Windows path with its separators eaten",
    why: "v1.19.0 shipped %LocalAppData%ProgramsAgent_bagentb.exe in two documents; a path with no separators is not a path",
    pattern: /%(?:LocalAppData|LOCALAPPDATA|ProgramFiles|USERPROFILE|ProgramData|AppData)%[A-Za-z]/,
    trips: "Run %LocalAppData%ProgramsAgent_bagentb.exe from a shell.",
    allows: "Run %LocalAppData%\\Programs\\Agent_b\\agentb.exe from a shell.",
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
