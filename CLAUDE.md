# Agent_b — worker instructions for Claude Code

@AGENTS.md

The file above is the repository guidance. It was written with Codex as the worker; every
sentence that says "Codex" applies to you. Everything else here is the same for both tools.

## How work arrives

- The planner (Fable) writes items under `plan/items/`, drops the order body into the repo-root
  `INBOX.md` under a `PUBLISH THEN EXECUTE` header, and the operator sends you a one-line
  pointer. You publish the order through `scripts/plan-publish.mjs prepare` / `publish`
  (validate first; a failed validation is a stop with the diagnostic, not a workaround), then
  execute the published `## Current work order` from `PLAN.md`.
- `REVISE` and `STOP` in `INBOX.md` work exactly as `AGENTS.md` describes. You never write
  instructions into `INBOX.md`; you acknowledge and truncate it.
- **INBOX takes a queue.** Several `PUBLISH THEN EXECUTE` blocks may be queued, separated by a
  line that is exactly `==== NEXT ORDER ====`. `node scripts/plan-publish.mjs split-inbox --inbox
  INBOX.md --out <scratch>` writes each block and its body in order. Publish and execute them in
  order in one session, each dry-run, validated, published and released on its own; a
  model-unavailable card in one order is recorded in its report and does not stop the next. Stop
  only on a hard stop or an empty queue. Truncate `INBOX.md` once every block has been read.

## INBOX.md — the operator's mailbox

Moved whole from PLAN.md's standing rules on 2026-09-19 (item 1c):

- **INBOX.md — the operator's mailbox.** At the repo root, gitignored, normally zero bytes. Read
  it (one file read, never a directory listing) at every boundary: the start of each W item,
  before any commit, before any hard-stop report, before every test or verification run, and
  after every ten file edits. Report the count and token cost of INBOX reads in each order's
  report; label token cost as estimated unless measured. Non-empty → the first line is the verb,
  the rest is payload:
  - `STOP` — finish the file in hand safely, report, end.
  - `REVISE: <text>` — the order changed; re-read the order you were given and its item files,
    continue.
  - anything else — a note; acknowledge it.
  Append one ack line to NOTES.md, then clear only the message you read; if the file changed,
  read the new message rather than erasing it. Only the operator or planner authors messages;
  your only write is clearing an acknowledged message. A message is ≤ 40 lines / 4 KB; your ack is one line. OUTBOX rotates
  at 200 lines.
- Append your report to `NOTES.md` under a new heading; never rewrite an existing section.

## Reporting cadence

- One report per order, at the close, appended to `NOTES.md` — or at a hard stop. Do not pause
  between W steps to summarise progress or ask whether to continue; the order is the answer.
  Progress lives in the markers in `plan/_inflight.md`, which the operator can read at any time.
- The only mid-order questions are hard stops. A refuted inference or an unmet condition stops
  that step, is recorded, and the next independent step begins.

## Markers

- Before any step, read `plan/_inflight.md` (PLAN.md's `## In flight` points there; closed
  orders' markers are in `plan/_history.md`). A step with a start marker and no completion marker
  is running; a completed step is history. Append your own `started` / `completed` / `stopped`
  lines to `plan/_inflight.md` with the current clock, never backfilled.

## Hard stops

Assets the operator supplies (images, fonts, fixtures) are the operator's: use them where the
item says and ask no licence question. `web/assets/source/` is gitignored — source images stay
local; only the derived assets the pipeline generates from them are committed.

The standing rules in `PLAN.md` list them. The ones you will meet most: UAC or elevation, or any
interactive Windows security prompt on the operator's desktop; touching production (port 8790);
the model server; deleting, recreating or rewriting this repository or its history; **locking,
disconnecting or switching the operator's session** — lock/disconnect proofs are the operator's
to schedule, never run unattended; anything the order names as operator-only. A hard stop is a
report and an end, not a question mid-run.

## Remote

The repository's remote is `https://github.com/rkclayton/agent_b.git`. It is the only remote.
