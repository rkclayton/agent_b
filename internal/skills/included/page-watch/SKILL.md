---
name: page-watch
description: Watch a web page or feed and tell the user only when the part they care about changes. Use for "tell me when", new posts, stock, status or availability.
state: true
---
# Page watch

One watch is one file in this skill's STATE FOLDER, `<slug>.json`:

```json
{"name":"", "url":"", "mode":"feed|article|text", "watch":"<what to look at, in the user's words>",
 "kind":"appears|disappears|changes|new-items", "value":"<the part as last seen>",
 "seen":["<item ids or titles, newest 100>"], "misses":0, "last_at":"", "job":"page-watch <slug>"}
```

## Start a watch (the user is here)
1. You need the address and WHAT on it matters: a phrase appearing ("in stock"), a phrase going away ("sold out"), a value changing (a date, a status, a version), or new items in a list or feed. If it is unclear, ask once.
2. `fetch_url` it: `mode:"feed"` for RSS or Atom, `mode:"article"` for a page of prose, no mode otherwise.
3. Find the part. Not in the text, or blocked: say so and stop. Create nothing.
4. Save the part as `value` (or the items as `seen`). Tell the user what you see now.
5. `cronjob {"action":"create","name":"page-watch <slug>","schedule":"every 6h","skills":["page-watch"],"prompt":"Check page watch <slug>."}`. Never more often than hourly.

## Check a watch (scheduled; nobody is here)
1. Read the file. Fetch the address once, the same way.
2. Failed or the part is gone from the text: add 1 to `misses`, save; at exactly 4 answer "page-watch <name>: I have not been able to read this page for a day."; otherwise `[SILENT]`.
3. Compare only the watched part. Ignore dates, counters, adverts and anything outside it.
4. Changed as `kind` says: answer in at most three lines — what changed, from what to what (or the new items' titles, at most 5), the address. Save the new `value` or `seen`.
5. Not changed: answer exactly `[SILENT]`.

## Other requests
- "what are you watching": one row per file.
- "stop watching X": `cronjob {"action":"remove","job_id":"page-watch <slug>"}`, then delete the file.

At most 20 watches. One fetch per check. Never sign in, never submit a form.
