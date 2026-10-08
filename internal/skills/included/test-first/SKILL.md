---
name: test-first
description: Write the failing test before the code. Use when adding a feature or fixing a known bug in a project that has tests.
---
# Test first

1. Find how this project runs tests and where they live (`search` for an existing test near the code). Use its style.
2. RED — write one test for the next small behaviour. Run it. It must fail, and fail for the right reason (not a typo or a missing import). Show the failure.
3. GREEN — write the least code that makes it pass. Run it. Run the neighbours.
4. TIDY — only now, and only if needed: remove repetition, improve a name. Run again.
5. Next behaviour: back to 2.

## Rules
- A bug: the first test reproduces it and fails before the fix.
- One behaviour per test. The test's name says the behaviour.
- Never change a test to match code you just wrote. If the test was wrong, say why before changing it.
- No test framework in the project: say so and ask before adding one.
- Finish with: the tests added, the command, the result.
