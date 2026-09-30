# Skills
A skill is a folder under the active profile's `skills` folder. It contains `SKILL.md` and may contain supporting files beside it. `SKILL.md` starts with YAML frontmatter. `name` is 1–64 lowercase letters, digits, or hyphens. `description` is 1–1024 characters and should say what the skill does and when to use it.
```markdown
---
name: report-kit
description: Builds a consistent report when a task asks for an invented fixture report.
---
Follow the report procedure here.
```
Only one index line is placed in the system prompt for each enabled skill: its name, description, and absolute `SKILL.md` path. The prompt tells the agent to read that file with `read_file` when the description matches. Supporting files stay out of the prompt and are read only when `SKILL.md` names them. A script is run only when the skill explicitly says to run it.
In Settings → Profiles → Skills, import a folder or rescan folders placed on disk, review its contents and warnings, then enable it. Imported and dropped-in skills start disabled. Treat third-party skills as code: enabling one grants no permissions, and its scripts remain under the ordinary command approval policy. Runs can read an enabled skill but cannot write into the profile skills folder.
Keep `SKILL.md` under 500 lines. Use forward-slash references at most one directory below it. Put a contents heading in references longer than 100 lines. These are row warnings rather than load failures.
Before relying on a skill, try at least three evaluation scenarios: a direct request that should select it, a differently worded request that should still select it, and a nearby request that should not select it. Confirm that a new chat reads `SKILL.md` without being told its path, reads only named supporting files, and follows the procedure.
