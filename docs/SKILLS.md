# Skills
A skill is a folder under the active profile's `skills` folder. It contains `SKILL.md` and may contain supporting files beside it. `SKILL.md` starts with YAML frontmatter. `name` is 1–64 lowercase letters, digits, or hyphens. The bundled `agent_b` skill also accepts its underscore. `description` is 1–1024 characters and should say what the skill does and when to use it.
```markdown
---
name: report-kit
description: Builds a consistent report when a task asks for an invented fixture report.
---
Follow the report procedure here.
```
Only one concise index line is placed in the system prompt for each enabled skill. A shared root plus the line's `i` or `u` marker identifies the exact `SKILL.md`; `s` identifies a private state folder. The prompt tells the agent to read that file with `read_file` when the description matches. Supporting files stay out of the prompt and are read only when `SKILL.md` names them. A script is run only when the skill explicitly says to run it.

Fresh profiles include and enable these skills:

- `action-items` — turn source material into decisions, actions, open questions, and parked work.
- `agent_b` — look up how the app's credentials, connectors, skills, jobs, notifications and Settings work.
- `debug` — reproduce and locate a bug before making the smallest fix.
- `ledger` — file purchases and bills from pasted text or a read-only Gmail connector, answer spending questions, and report expiring or untouched food once daily.
- `page-watch` — report only a watched page or feed change.
- `price-watch` — watch one price, compare confirmed sellers for an exact product, or alert when it appears.
- `research` — answer current questions from several numbered sources.
- `review` — inspect changed code and report findings without fixing it.
- `summarize` — summarize complete files, pages, or pasted text faithfully.
- `test-first` — add a failing test before implementing a behavior.
- `write-skill` — turn a repeated task into a skill folder.

Use the row's switch to turn an included skill off. That choice survives restarts and updates. An ordinary skill folder with the same name replaces an included skill. Removing the ordinary folder reveals the current included copy again. Updates replace only included copies and preserve replacements, switches and state. The `ledger`, `page-watch`, and `price-watch` skills each receive a durable state folder capped at 16 MiB.

<!-- agent-help: Skills -->
In **Settings → Skills**, choose the agent profile, import a folder or rescan folders placed on disk, review its contents and warnings, then enable it. Imported and dropped-in skills start disabled. A new chat indexes the enabled skills and reads the matching `SKILL.md` as needed. Scripts keep the ordinary command approval policy.
<!-- /agent-help -->

<!-- agent-help: Scheduled jobs -->
Ask in chat to schedule something and give the task and timing. `cronjob` accepts schedules such as `in 30m`, `every 6h`, or `weekdays at 9am`. It can list, update, pause, resume, run or remove a job by name or ID. Name the skills a scheduled run needs. A job runs in a fresh chat and can deliver its result through the chosen notification channel.
<!-- /agent-help -->

<!-- agent-help: Notifications -->
Open **Settings → Notifications** to configure delivery. A successful scheduled check that returns only `[SILENT]` or an empty reply sends no notice. Scheduled phone pushes name the job and its outcome. Desktop and webhook notices can include the first two answer lines, capped at 300 characters.
<!-- /agent-help -->
Drafts made in chat live in the operator's `Agent_b\skills\<name>\` folder until the Add skill card is approved; earlier `skill-proposals` drafts are moved there once on upgrade.
Keep `SKILL.md` under 500 lines. Use forward-slash references at most one directory below it. Put a contents heading in references longer than 100 lines. These are row warnings rather than load failures.
Before relying on a skill, try at least three evaluation scenarios: a direct request that should select it, a differently worded request that should still select it, and a nearby request that should not select it. Confirm that a new chat reads `SKILL.md` without being told its path, reads only named supporting files, and follows the procedure.
