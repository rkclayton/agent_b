import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdir, mkdtemp, readdir, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { basename, dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { removeTreeWithinAllowedRoots } from "../tools/removal-guard.mjs";

const repoRoot = dirname(dirname(fileURLToPath(import.meta.url)));

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
  "tests/README.md",
];

// rel-1.24.0: AGENTS.md and CLAUDE.md left the public repository at the
// operator's request. They are still on his disk and still read by the product,
// so they are still checked for vocabulary WHEN PRESENT — but a CI checkout does
// not have them, and a list that requires them turns every checkout red.
const OPERATOR_LOCAL_DOCUMENTS = ["AGENTS.md", "CLAUDE.md"];

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

// The vocabulary rules below run over BOTH lists: a document that is present is
// checked whether or not the repository ships it. Only the existence assertion
// distinguishes them.
const sources = Object.fromEntries(await Promise.all([...documents, ...OPERATOR_LOCAL_DOCUMENTS].map(async (name) => {
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

// Item 2mj: THE PRODUCT CALLS ITSELF agent_b IN ITS OWN VOICE.
//
// The W0 classification is the item's real output, and it is what this gate
// encodes. Twelve tracked files named a vendor and only ONE of them was the
// product talking about itself — web/plan.html said "You and Claude write items".
// The other eleven are third-party facts or historical operator data, and a gate
// that blanket-skipped whole files would pass the next leak, so each exception is
// narrow and carries its reason.
//
// Three classified exceptions:
//   1. OpenAI-compatible — the wire protocol's own name, in nine files.
//   2. A historical worker label in a stop-discipline fixture: operator data.
//   3. The worker executable's name in the queue runner's default: a third-party
//      fact about the command being run, not the product's voice.
const VENDOR_GREP = "Codex|Claude|Fable|Anthropic|OpenAI|ChatGPT|GPT-[0-9]";
const VENDOR_WORDS = /\b(Codex|Claude|Fable|Anthropic|ChatGPT|GPT-[0-9])\b/;

// `OpenAI` appears only ever as part of the protocol's name. It is matched on its
// own so a bare "OpenAI" in the product's voice would still be caught.
// The protocol's name appears as "OpenAI-compatible" and, where INTERFACES.md
// describes the request bodies, "OpenAI-shaped". Both are facts about the wire,
// so both are stripped and a bare mention is still caught.
const PROTOCOL_NAME = /OpenAI-(?:compatible|shaped)/g;

const VOICE_EXCEPTIONS = new Map([
  ["tests/stop-discipline/known-cases/v0.18.0-w9.json", "a historical worker label recorded in a fixture: operator data, not the product's voice"],
  ["tools/run-queue.ps1", "the worker executable's name as the runner's default command: a third-party fact"],
  ["tests/docs-terms.test.mjs", "this gate names the words it forbids"],
]);

test("no shipped file names a model vendor in the product's own voice", async () => {
  const listed = execFileSync("git", ["grep", "-lwIE", VENDOR_GREP, "--", "."], { cwd: repoRoot, encoding: "utf8" })
    .split("\n").map((line) => line.trim()).filter(Boolean);
  const offenders = [];
  for (const relative of listed) {
    if (VOICE_EXCEPTIONS.has(relative)) continue;
    const text = await readFile(join(repoRoot, relative), "utf8");
    // Strip the protocol's name, which is a fact about the endpoint being spoken
    // to. What is left must name no vendor at all.
    const remaining = text.replace(PROTOCOL_NAME, "").replace(/\bOpenAI endpoint\b/g, "");
    const hit = VENDOR_WORDS.exec(remaining) || /\bOpenAI\b/.exec(remaining);
    if (hit) offenders.push(`${relative}: ${hit[0]}`);
  }
  assert.deepEqual(offenders, [], `these tracked files name a vendor outside the three classified exceptions:\n${offenders.join("\n")}`);
});

test("the gate fails on a seeded vendor name and passes the classified exceptions", () => {
  // Seeded: the exact shape of the defect this item fixes.
  assert.match("You and Claude write items", VENDOR_WORDS);
  // Exception 1: the protocol's name survives the strip and then matches nothing.
  assert.doesNotMatch("Connections target an OpenAI-compatible server".replace(PROTOCOL_NAME, ""), /\bOpenAI\b/);
  // A bare vendor name is still caught even where the protocol's name is allowed.
  assert.match("an OpenAI-compatible server built by OpenAI".replace(PROTOCOL_NAME, ""), /\bOpenAI\b/);
  // Exceptions 2 and 3 are named, not skipped by path shape.
  assert.equal(VOICE_EXCEPTIONS.size, 3);
  for (const reason of VOICE_EXCEPTIONS.values()) assert.ok(reason.length > 20, "every exception states why");
});

// Item 2n9: RELEASE NOTES ARE WRITTEN TO ANY USER. The operator, 2026-09-27: the
// notes "are a bit too personal and toward the wrong audience — they reference
// setting up a model on my PC for testing etc. These should be aimed at a general
// audience."
//
// The gate itself lives in tools/check-release-notes.mjs because deploy-release
// calls it before staging; this holds it to its own contract and holds every note
// written under the rule to the gate.
test("every release note under the rule passes the notes gate", async () => {
  const { notesUnderTheRule, checkNotes, bannedPatterns } = await import("../tools/check-release-notes.mjs");
  const patterns = bannedPatterns();
  const offenders = [];
  for (const file of notesUnderTheRule()) {
    const failures = checkNotes(file, patterns);
    if (failures.length) offenders.push(`${basename(file)}: ${failures.map((f) => `line ${f.line} ${f.kind} ${f.entry}`).join("; ")}`);
  }
  assert.deepEqual(offenders, [], `notes written to the wrong reader:\n${offenders.join("\n")}`);
});

test("the notes gate catches what the item's evidence quoted, and the list is one editable file", async () => {
  const { checkNotes, bannedPatterns, underTheRule, versionOf } = await import("../tools/check-release-notes.mjs");
  const { mkdtemp, writeFile } = await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const patterns = bannedPatterns();
  const root = await mkdtemp(join(tmpdir(), "agentb-notes-gate-"));
  const seed = async (body) => {
    const file = join(root, "v9.9.9.md");
    await writeFile(file, body, "utf8");
    return checkNotes(file, patterns);
  };

  // The exact shapes 2n9's evidence quoted from the old notes.
  for (const line of [
    "- the check was made again and the Ollama machine still lists nothing",
    "- yours is named `scratch`",
    "- passing for twelve releases",
    "- the document another repository was waiting on",
    "- the operator pressed Update",
  ]) {
    const failures = await seed(`# Agent_b v9.9.9\n\n## Fixed\n\n${line}\n`);
    assert.ok(failures.some((f) => f.kind === "vocabulary"), `not caught: ${line}`);
  }

  // (g): narrative outside a bullet fails even when every word is allowed.
  const prose = await seed("# Agent_b v9.9.9\n\n## Fixed\n\nThe update button works now, and here is the story of how.\n");
  assert.ok(prose.some((f) => f.kind === "prose"), "a narrative paragraph was allowed");

  // A note that obeys both rules passes.
  assert.deepEqual(await seed("# Agent_b v9.9.9\n\n## Fixed\n\n- The Update button works.\n- A failed update says why.\n"), []);

  // Notes older than the rule are left as the record they are.
  assert.equal(underTheRule("v1.23.0.md"), false);
  assert.equal(underTheRule("v1.24.0.md"), true);
  assert.deepEqual(versionOf("v1.27.0.md"), [1, 27, 0]);
});

// Item 2na (d): A SPAWN SITE THAT CAN TAKE THE OPERATOR'S SCREEN FAILS THE SUITE.
//
// He works on this machine while the suite runs. Thirteen scripts under tools/,
// scripts/ and tests/ start processes and eleven had remembered to hide the window;
// the fourteenth is the problem, and a person remembering is not a mechanism. This
// gate greps the spawn set and fails on a Start-Process that neither hides its window
// nor appears below as a deliberate exception with its reason.
//
// The exceptions are deliberate and each is a window the operator is MEANT to see.
// Adding one means writing down why, here, where the next reader will find it.
const spawnExceptions = new Map([
  [
    "scripts/launch-Agent_b.ps1:Start-Process $Url",
    "opening the browser at Agent_b is the point of a launch, and -NoBrowser is how a caller declines it",
  ],
]);

// A spawn is quiet when it hides its window, reuses the current console rather than
// opening one — which is what -NoNewWindow does for the deliberate foreground start,
// whose window is the operator's and whose closing is how he stops the server —
// or goes through the Start-Quiet helper.
const quietMarkers = ["-WindowStyle Hidden", "WindowStyle = 'Hidden'", "-NoNewWindow", "Start-Quiet", "Start-QuietMinimized"];

test("no spawn site in the tooling can take the operator's screen", async () => {
  const root = repoRoot;
  const directories = ["tools", "scripts", "tests"];
  // windows-tools.ps1 DEFINES the quiet start: its own Start-Process calls are
  // where the window style is set, from a splat this grep cannot read.
  const definesTheHelper = "scripts/windows-tools.ps1";
  const offenders = [];
  const unusedExceptions = new Set(spawnExceptions.keys());

  for (const directory of directories) {
    let names;
    try {
      names = await readdir(join(root, directory));
    } catch {
      continue;
    }
    for (const name of names.filter((one) => one.endsWith(".ps1"))) {
      const relative = `${directory}/${name}`;
      if (relative === definesTheHelper) continue;
      const text = await readFile(join(root, directory, name), "utf8");
      const lines = text.split(/\r?\n/);
      for (const [index, line] of lines.entries()) {
        const trimmed = line.trim();
        // A comment about spawning, or a gate grepping for one, is not a spawn.
        if (!trimmed.includes("Start-Process") || trimmed.startsWith("#")) continue;
        if (trimmed.includes("'Start-Process") || trimmed.includes('"Start-Process')) continue;
        // The call may wrap, so the quiet marker is looked for on the statement,
        // which continues while the line ends in a backtick or an open splat.
        let statement = trimmed;
        for (let ahead = index + 1; ahead < lines.length && /[`@{,]$/.test(statement.trim()); ahead += 1) {
          statement += " " + lines[ahead].trim();
        }
        if (quietMarkers.some((marker) => statement.includes(marker))) continue;
        const key = `${relative}:${trimmed}`;
        if (spawnExceptions.has(key)) {
          unusedExceptions.delete(key);
          continue;
        }
        offenders.push(`${relative}:${index + 1}: ${trimmed}`);
      }
    }
  }

  assert.deepEqual(
    offenders,
    [],
    `these spawn sites can put a window on the operator's screen. Start them through Start-Quiet in scripts/windows-tools.ps1, or add the site to spawnExceptions with the reason the window is meant to be seen:\n${offenders.join("\n")}`,
  );
  // An exception that no longer matches anything is a stale note, and a stale note is
  // how the list stops being trustworthy.
  assert.deepEqual([...unusedExceptions], [], "these documented spawn exceptions match no site any more and should be removed");
});

test("every console child started by the product is no-window 2pp", async () => {
  const checks = [
    ["internal/signing/manager_windows.go", /exec\.CommandContext[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["internal/detection/detection.go", /exec\.CommandContext[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["internal/reflection/structure.go", /exec\.CommandContext[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["internal/updater/signature_windows.go", /exec\.CommandContext[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["internal/web/speech.go", /quietproc\.Quiet\(exec\.CommandContext[\s\S]*quietproc\.Quiet\(command\)/],
    ["internal/tools/sandbox.go", /quietproc\.Quiet\(exec\.CommandContext/],
    ["internal/tools/call_service.go", /exec\.CommandContext[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["internal/tools/proc_windows.go", /CREATE_NEW_PROCESS_GROUP \| createNoWindow[\s\S]{0,300}HideWindow: true/],
    ["internal/modelinstall/start_windows.go", /CreationFlags: 0x08000000, HideWindow: true/],
    ["internal/updater/manager.go", /quietproc\.Quiet\(exec\.Command/],
    ["cmd/harness/install_native_windows.go", /exec\.Command[\s\S]{0,300}quietproc\.Quiet\(command\)/],
    ["cmd/harness/install_detach_windows.go", /CreationFlags \|= syscall\.CREATE_NEW_PROCESS_GROUP/],
    ["scripts/launch-Agent_b.ps1", /Start-Process -FilePath 'powershell\.exe' -ArgumentList \$arguments -WindowStyle Hidden/],
  ];
  for (const [relative, pattern] of checks) {
    assert.match(await readFile(join(repoRoot, relative), "utf8"), pattern, relative);
  }
  const install = await readFile(join(repoRoot, "cmd/harness/install_mode.go"), "utf8");
  assert.equal((install.match(/quietproc\.Quiet\(command\)/g) || []).length, 4, "every PowerShell installer child is quiet");
});

// Item 2nj: A SWITCH PASSED AS QUOTED TEXT IS A BINDING ERROR, AND IT KILLED EVERY RUN.
//
// `'-Confirm:$false'` in single quotes is literal text. The child bound the string
// "$false" to a SwitchParameter and died with "Cannot convert 'System.String' to the type
// 'SwitchParameter'" before it touched the account — so every Repair and every Set up
// through that script failed from the day the argument was written, and until item 2ng
// captured the elevated child's streams nobody could see why.
//
// This is a source check because the failure was a source mistake: a quoted switch looks
// exactly like a working argument until something runs it.
test("no script passes a PowerShell switch as quoted text", async () => {
  const offenders = [];
  for (const directory of ["scripts", "tools", "tests"]) {
    let names;
    try {
      names = await readdir(join(repoRoot, directory));
    } catch {
      continue;
    }
    for (const name of names.filter((one) => one.endsWith(".ps1"))) {
      const text = await readFile(join(repoRoot, directory, name), "utf8");
      for (const [index, line] of text.split(/\r?\n/).entries()) {
        const trimmed = line.trim();
        // A comment explaining the mistake is not the mistake.
        if (trimmed.startsWith("#")) continue;
        // Only where an argument LIST is being built. A quoted switch passed as DATA is
        // fine and there is one: the service-account setup path hands the name of a parameter
        // to an error message, which is not an argument being forwarded to anything.
        if (!/@\(|\+=|-Argument(?:List)?\b|\bArguments\b/.test(trimmed)) continue;
        // `'-Switch:$true'` or `"-Switch:$false"` inside that list.
        for (const match of trimmed.matchAll(/(['"])(-[A-Za-z]+:\$(?:true|false))\1/g)) {
          offenders.push(`${directory}/${name}:${index + 1}: ${match[2]} is quoted, so the child receives literal text`);
        }
      }
    }
  }
  assert.deepEqual(
    offenders,
    [],
    `these pass a switch as text; the child binds the word "$false" to a SwitchParameter and fails before doing anything:\n${offenders.join("\n")}`,
  );

  // AND THE CHECK CATCHES IT: the line that actually shipped, seeded here, so a gate that
  // has quietly stopped matching cannot pass on an empty sweep.
  const shipped = "$accountArguments = @('-AccountName', $AccountName, '-CredentialStore', $machinePath, '-NoPrompt', '-Confirm:$false')";
  const quoted = [...shipped.matchAll(/(['"])(-[A-Za-z]+:\$(?:true|false))\1/g)];
  assert.equal(quoted.length, 1, "the pattern no longer matches the line that shipped");
  assert.equal(quoted[0][2], "-Confirm:$false");
  assert.ok(/@\(|\+=|-Argument(?:List)?\b|\bArguments\b/.test(shipped), "the argument-list narrowing would have skipped the real mistake");
  // And the data use is NOT matched, which is why that narrowing exists at all.
  const dataUse = "Assert-SafeInteractiveInput -NonInteractiveParameter '-Confirm:$false'";
  assert.equal(/@\(|\+=|-Argument(?:List)?\b|\bArguments\b/.test(dataUse), false);
});

// Item 2o5 (e): CI has no client deny-list (it lives outside the repository), so it
// proves only that the gate catches a term planted in a temporary fixture.
test("the client-terms gate catches a planted term", () => {
  const output = execFileSync(process.execPath, [join(repoRoot, "tools", "check-client-terms.mjs"), "--self-test"]).toString();
  assert.match(output, /a planted term was caught/);
});

test("the client-terms gate scans publishable paths, not checkout parent folders", async () => {
  const { publicationEntry } = await import("../tools/check-client-terms.mjs");
  const { scanTextEntries } = await import("../tools/privacy-gate.mjs");
  const term = ["listed", "standin"].join("-"), terms = [{ term, listLine: 1 }], outside = await mkdtemp(join(tmpdir(), "agentb-terms-")), root = join(outside, term, "repository");
  try {
    await mkdir(root, { recursive: true });
    const findings = async (name, text) => { const file = join(root, name); await writeFile(file, text); return scanTextEntries([publicationEntry(file, root)], terms).map(({ name: found, rule }) => ({ name: found, rule })); };
    assert.deepEqual(await findings("clean.txt", "publishable bytes\n"), []);
    assert.deepEqual(await findings("content.txt", `publishable ${term} bytes\n`), [{ name: "content.txt", rule: "outside-list" }]);
    assert.deepEqual(await findings(`${term}.txt`, "publishable bytes\n"), [{ name: "path-name", rule: "outside-list" }]);
  } finally { removeTreeWithinAllowedRoots(outside, [tmpdir()], "client terms fixture cleanup"); }
});

// rel-1.42.0 Misses: the tag is refused unless buildinfo and the installer name it.
test("the tag gate refuses a tag the tree's build does not report", async () => {
  const { releaseIdentity, tagRefusal } = await import("../tools/tag-release.mjs");
  const identity = releaseIdentity();
  assert.equal(tagRefusal(identity.buildinfo, identity), "");
  assert.match(tagRefusal("v0.0.1", identity), /buildinfo reports/);
  assert.match(tagRefusal(identity.buildinfo, { ...identity, display: "0.0.1" }), /installer displays/);
});

test("the telemetry release gate names a refused or dropped event before tagging", async () => {
  const { createServer } = await import("node:http");
  const { releaseAfterTelemetry } = await import("../tools/tag-release.mjs");
  for (const response of [
    { status: 422, body: { error: "unknown_event_type" } },
    { status: 202, body: { stored: 1, dropped: ["run.summary"] } },
  ]) {
    const server = createServer((request, answer) => {
      assert.equal(request.headers["x-agentb-synthetic"], "1");
      answer.writeHead(response.status, { "content-type": "application/json" });
      answer.end(JSON.stringify(response.body));
    });
    await new Promise(resolve => server.listen(0, "127.0.0.1", resolve));
    let tagged = false;
    try {
      await assert.rejects(
        releaseAfterTelemetry(() => { tagged = true; }, { endpoint: `http://127.0.0.1:${server.address().port}`, vectors: [{ type: "run.summary", at: "2026-10-06T00:00:00Z" }] }),
        /run\.summary/,
      );
      assert.equal(tagged, false);
    } finally {
      await new Promise(resolve => server.close(resolve));
    }
  }
});

test("an unreachable telemetry intake is reported external and does not block tagging", async () => {
  const { releaseAfterTelemetry } = await import("../tools/tag-release.mjs");
  let tagged = false;
  const lines = [];
  await releaseAfterTelemetry(() => { tagged = true; }, {
    endpoint: "http://127.0.0.1:1",
    vectors: [{ type: "run.summary", at: "2026-10-06T00:00:00Z" }],
    report: line => lines.push(line),
  });
  assert.equal(tagged, true);
  assert.match(lines.join("\n"), /not exercised: external.+reason/i);
});
