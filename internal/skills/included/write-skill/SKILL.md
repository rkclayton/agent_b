---
name: write-skill
description: Write a new skill for Agent_b from a task the user repeats. Use for "make a skill" or "do it this way every time".
---
# Write a skill

A skill is a folder holding `SKILL.md`. Only its name and description are in front of the model; the rest is read when the description matches. So the description decides whether it is ever used.

1. Ask what the task is and when it should be used, if the user has not said. Look at how it was done in this chat if it was.
2. Make a folder in the working folder named for the skill: lowercase letters, digits and hyphens.
3. Write `SKILL.md`:

```markdown
---
name: <folder name>
description: <what it does>. Use when <the words or situations that should pick it>.
---
# <Title>
<numbered steps, each one action, naming the tool to use>
## Rules
<what must never happen>
```

4. Description: one line, under 160 characters, what it does AND when to use it, in the words the user would say.
5. Steps: short, in order, one action each. Exact names, formats and commands. Say what to do when a step fails.
6. Keep it under 150 lines. Longer material goes in a second file beside it, named in the step that needs it.
7. No script unless the user asks for one; a script is run only when the skill says to.
8. Tell the user: the folder's path, and "Settings → Profiles → Skills → import this folder, then switch it on."

## Check it
Give the user three things to try in a new chat: a request that should pick the skill, the same request in other words, and a nearby request that should NOT pick it.
