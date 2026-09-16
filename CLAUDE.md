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
- Append your report to `NOTES.md` under a new heading; never rewrite an existing section.

## Markers

- Before any step, read `## In flight` in `PLAN.md`. A step with a start marker and no
  completion marker is running; a completed step is history. Append your own `started` /
  `completed` / `stopped` lines under `## In flight` with the current clock, never backfilled.

## Hard stops

The standing rules in `PLAN.md` list them. The ones you will meet most: UAC or elevation;
touching production (port 8790); the model server; deleting, recreating or rewriting this
repository or its history; anything the order names as operator-only. A hard stop is a report
and an end, not a question mid-run.

## Remote

The repository's remote is `https://github.com/rkclayton/agent_b.git`. It is the only remote.
