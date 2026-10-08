---
name: debug
description: Find the cause of a bug before changing anything. Use when a test fails, a program errors or crashes, or something "doesn't work" and the cause is not known.
---
# Debug

## 1. Reproduce
- Run the exact command that fails. Copy the exact error and the first frame of the trace that is in this project's code.
- Cannot reproduce: say so and stop. Do not fix what you cannot see fail.

## 2. Locate
- `read_file` the failing line and the function around it. Follow the bad value BACKWARD to where it was first wrong.
- One guess at a time. Write it in one sentence: "X is nil because Y returns early when Z".
- Test the guess with the smallest check — a print, a single test, one command — before changing code.
- Guess wrong: undo the check, make the next guess. Do not stack guesses.

## 3. Fix
- Change the cause, not the place it showed. The smallest change that makes it right.
- Nothing else in the same change: no renames, no tidying, no extra features.
- Never edit a test so it passes, loosen a check, or catch-and-ignore an error to make the message go away.

## 4. Verify
- Run the command from step 1. Then the tests next to what you changed.
- Remove every print and check you added.
- Report in three lines: the cause; the change (file and line); the proof it ran.

Two fixes in a row did not work: stop, undo them, and go back to step 2 with what they taught you.
