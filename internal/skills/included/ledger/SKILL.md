---
name: ledger
description: File purchases and bills, answer price and spending questions, and give one daily notice for expiring or untouched food. Use for receipts, orders, purchases, inventory, bills, or meal ideas.
state: true
---
# Ledger

Keep all files in this skill's STATE FOLDER. Dates are the PC's local `YYYY-MM-DD`; never store times.

- `ledger.jsonl`: one compact line per purchase: `{"item":"","qty":1,"unit":"","price":0,"store":"","bought":"","category":"food|household|other","use_by":null,"source":"paste"}`. Append new rows; never rewrite earlier rows.
- `ledger.csv`: the same rows and order, headed `item,qty,unit,price,store,bought,category,use_by,status`. Append intake rows and replace only a changed row; keep RFC 4180 quoting. The first intake reply names this path once.
- `items.json`: one key per normalized item name with `{category,shelf_days?,burn_days?,last_seen_used?,like?,notes?,season?}`.
- `asked.json`: one key `<item>|<question>` with the date last asked.
- `bills.json`: one key per bill with `{amount?,due_day,last_paid?,notes?}`.

Read the files before changing them. Create missing files with empty objects or the CSV header. Keep `items.json`, `asked.json`, and `bills.json` compact. Use `edit_file` to append one JSONL/CSV row or replace one existing entry; never re-encode the purchase history for one change.

## Categories and shelf life

Food classes are dairy, meat, produce, frozen, pantry, and dry goods. Use the shortest obvious class from the receipt text. Frozen defaults to 90 shelf days and dry goods to 365. Use a saved `shelf_days` for other known food. Household supplies are `household`; all remaining purchases are `other`. Non-food always has `use_by:null`. Unknown food has `use_by:null` and queues one missing-shelf-life yes/no question for the daily check.

## Intake — the user is here

For pasted receipt or order text:

1. Extract one row per line item. Preserve the receipt's store text, purchase date, quantity, unit, and line price; do not invent missing values.
2. Set category with the table above. For food only, set `use_by` to purchase date plus known shelf days. Queue only an unknown food's one shelf-life question.
3. Append each compact JSONL row and matching CSV row. Update only affected item entries.
4. If no job named `ledger daily` exists, call `cronjob {"action":"create","name":"ledger daily","schedule":"every day at 8am","skills":["ledger"],"prompt":"Ledger daily check."}`. Never create a second one.
5. Reply on one line: `filed N items from <store> on <date>, <total>; first to go: <item> on <date>`. Omit the final clause when no row has `use_by`. On the first intake only, append `; CSV: <path>`.

## Daily check — scheduled; nobody is here

Read all five files. Never use `ask` in a scheduled run.

- EXPIRING: food rows not used or gone whose `use_by` is within three days.
- UNTOUCHED: food bought at least 21 days ago, not past `use_by`, with no `last_seen_used`.
- BILLS: due within three days and not paid in the current month. Write `<bill> due <date>` after expiring rows.
- QUESTIONS: at most five yes/no questions not present in `asked.json` within seven days: a missing shelf life; `still have the <item>?` for untouched food; or `did the <item> last about N days?` for an unconfirmed burn estimate. Record each question's date when asked.

Past `use_by` plus seven days with no answer marks the row `gone` and asks nothing. A no to `still have` marks it used. After two no answers to the same burn estimate, calculate `burn_days` from purchase/use dates and stop asking it.

If every list is empty, answer exactly `[SILENT]`. Otherwise answer at most eight lines. Line one is `ledger: <n> expiring, <m> untouched, <b> bills due`; then one `<item> — <date>` per expiring or untouched row, bill lines, and up to five questions. Non-food never appears except as a due bill.

## Requests — the user is here

- `what did I pay for <item>` or `price of <item>`: matching purchases newest first, each with change from the previous buy.
- `what did I spend this month`, `in <month>`, or `at <store>`: total, then the five largest line costs.
- `what do I have`: on-hand rows grouped by category.
- `what can I make`: at most five bowls using only on-hand food, each `base + protein + jar + vegetable`; omit a bowl if a named component is unavailable.
- `I like/don't like <item>` or `<item> is a <season> thing`: update that item's entry and reply on one line.
- `remind me <bill> on the <n>th`: create or update its bill, amount optional, and reply on one line. `paid <bill>` sets `last_paid` to today. `what's due` lists bills by next due date with amounts when known.

Never buy, sign in, or make a network call. Never name a store that was not in the user's text.
