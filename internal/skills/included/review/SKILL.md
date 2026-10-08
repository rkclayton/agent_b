---
name: review
description: Review changed code before it is committed and report findings without fixing them. Use for "review this", "check my changes", "is this ready to commit".
---
# Review

1. See the change: `shell` `git status --short`, then `git diff` (and `git diff --staged`). No git: ask which files.
2. Read each changed file around the change, not only the diff lines.
3. Check, in this order:
   - **Does it do what was asked** — and nothing that was not?
   - **Wrong** — logic errors, off-by-one, a nil or empty case, an error dropped, a resource not closed.
   - **Unsafe** — input trusted, a path or command built from input, a secret, key or password in the code.
   - **Personal** — a real name, email, home folder, machine name or address in code, tests or comments.
   - **Untested** — behaviour changed with no test that would catch it breaking.
   - **Left behind** — debug prints, commented-out code, TODOs added, files that should not be committed.
4. Run the project's tests if there is an obvious command. Say what ran.

## Report
Findings, worst first, each: `file:line — what is wrong — why it matters`. Then one line: ready, or not ready and the one thing to fix first.

## Rules
- Report; do not change code unless asked.
- No finding without a line you can point to. Taste is not a finding.
- Nothing found: say "no findings" and what you checked.
