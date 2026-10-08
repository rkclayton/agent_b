---
name: price-watch
description: Watch a product, fare or listing and tell the user only when its price meets their target. Use when asked to track, watch or alert on a price or a deal.
state: true
---
# Price watch

One watch is one file in this skill's STATE FOLDER, `<slug>.json`:

```json
{"name":"", "url":"", "currency":"USD", "target":900, "drop_pct":null,
 "baseline":{"price":0,"at":""}, "last":{"price":0,"in_stock":true,"at":""},
 "low":{"price":0,"at":""}, "alerted":null, "misses":0, "job":"price-watch <slug>",
 "history":[["<time>",0]]}
```

`target` is a price; `drop_pct` is a percent below `baseline`. At least one is set. `history` keeps the newest 60.

## Reading a price
1. `fetch_url` the address, once.
2. Take the price for the exact item and variant: structured data first (`"price"` with `priceCurrency`, `og:price:amount`, an `offers` block), then the price printed nearest the item's title.
3. Never take a crossed-out, "list", "was", "from", per-month or per-unit price.
4. No price in the text, or the fetch was refused or blocked: there is NO price. Do not guess. Do not try another way in.

## Start a watch (the user is here)
1. You need the exact address and a target price or a percent drop. If one is missing, ask for it once.
2. Read the price. No price: say "I can't read a price on that page — it needs a browser or it blocks me" and stop. Create nothing.
3. Say the price you found and ask nothing more.
4. Write the file with `baseline`, `last` and `low` set to that price.
5. `cronjob {"action":"create","name":"price-watch <slug>","schedule":"every 6h","skills":["price-watch"],"prompt":"Check price watch <slug>."}`. Never more often than hourly.
6. One line back: what is watched, the price now, the target, how often.

## Check a watch (scheduled; nobody is here)
1. Read the file. Read the price.
2. No price: add 1 to `misses`, save. At exactly 4 misses answer "price-watch <name>: I have not been able to read this page for a day." Otherwise answer exactly `[SILENT]`.
3. Price read: `misses` 0; update `last`, `low` and `history`; save.
4. ALERT if the price is at or under `target`, or at or under `baseline` less `drop_pct` percent — and `alerted` is null, or the price is lower than `alerted.price`, or `alerted.at` is more than 7 days old.
5. ALERT is two lines, then save `alerted`:
   `<name> — <price> <currency> (was <baseline>; lowest seen <low> on <date>)`
   `<url>`
6. No alert: answer exactly `[SILENT]`.

## Other requests
- "what are you watching": one row per file — name, price now, target, lowest seen.
- "stop watching X": `cronjob {"action":"remove","job_id":"price-watch <slug>"}`, then delete the file.
- "change the target": edit the file; set `alerted` to null.

At most 20 watches. One fetch per check. Never sign in, never add to a cart, never buy.
