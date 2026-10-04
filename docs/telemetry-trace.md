# The flight recorder — trace schema

Items 2pw and 2q6. This document **is** the schema for the flight recorder's
events. The PC builds them in `internal/recorder`; the broker and the phone
build from this file. A test parses every vector below and checks it against
the PC's own builder, so the two cannot drift apart.

`docs/TELEMETRY.md` is the envelope, the batch rules and the off switch; it
applies here unchanged. This file adds event types to it.

## What is never in a trace

The recorder stores no text. Not of a message, an argument, a tool result, a
path, a host, a chat title, or a name. Specifically:

- A tool argument is its **key**, its value's **length** in characters, and an
  8-hex **HMAC** of the value under a salt made for that run. The salt never
  leaves the process, so the HMAC says "the same value as before" and nothing
  else. A key that is not a plain lowercase identifier is sent as `<key>`.
- The model's output is reduced to one number, `repeat_4gram_ratio`: the share
  of its word 4-grams that repeat an earlier one.
- The prompt is reduced to `prompt_template_hash`: 8 hex of the SHA-256 of the
  base system template, so a template change is visible and its text is not.
- A local model's id is its **file name** only, never its folder.
- No id is longer than 8 characters. `run` and `report_id` are random and
  cannot be joined to a chat, a session or an install.

## Where it is kept

Every run of every chat is recorded, whether or not anonymous diagnostics are
on, in memory: the last **20 runs per chat** and **256 KiB** in all, oldest run
evicted first. One run keeps at most 32 KiB of spans; later spans are counted
in `spans_dropped`. Each event costs the same however much is stored: a span is
encoded once and appended. A chat's runs are deleted with the chat.

## `trace` — "Report this chat"

Sent only when someone right-clicks a chat tab and picks **Report this chat**,
and sent whether or not anonymous diagnostics are on: the click is the consent
for that one report. It is one batch of one event, at most **48 KiB**, under an
install id made for that report. It carries the chat's last **5 runs**; when
they do not fit, the oldest runs are dropped first. The page shows
`Reported — id <report_id> copied` and puts the id on the clipboard.

| Field | Type | Meaning |
|---|---|---|
| `report_id` | string | 8 hex, random; what the person pastes |
| `runs` | array | oldest first; each `{"spans": [...], "spans_dropped": int}` |

Each run's first span is `invoke_agent`; the rest are in the order they
happened, numbered by `seq`.

### Spans

Names follow the OpenTelemetry GenAI conventions where one exists.

| `span` | Fields |
|---|---|
| `invoke_agent` | `run` (8 hex), `gen_ai.provider.name` (`local` or `api`), `gen_ai.request.model` (file name), `context_size`, `prompt_template_hash`, `tools_offered` (names), `stop_reason` (absent while running) |
| `chat` | one inference: `seq`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.usage.cache_read.input_tokens` (absent when the server did not say), `context_fill_pct`, `ttft_ms` (absent when nothing streamed), `duration_ms`, `gen_ai.response.finish_reasons`, `tool_calls_since_user`, `repeat_4gram_ratio` |
| `execute_tool` | one tool call: `seq`, `gen_ai.tool.name`, `offered`, `args` (`key`, `len`, `hmac` each), `retry_index` (earlier identical calls in the run), `result` (`ok`, `empty`, an error class from docs/TELEMETRY.md, or `error`), `result_bytes`, `duration_ms`. A call to a tool that was not offered is `offered: false`, `result: "not_offered"` and nothing else. |
| `tool_parse_error` | `seq`, `class`: `arguments` (unparseable arguments), `malformed_tool_turn`, `truncated_tool_call`, `malformed_tool_history` |
| `loop` | `seq`, `gen_ai.tool.name`, `repeats`: the same tool with the same argument HMAC the third time in a run; follows that call's span |
| `condensed` | `seq`, `kind`, `trigger`, `before`, `after` |
| `context_exceeded` | `seq`, `reason` |
| `cancelled` | `seq`, `reason` (absent for an abort that did not stop the run) |

A tool name that is not a registered-tool shape is sent as `<invalid>`.

```json vector:trace
{"type":"trace","at":"2026-10-04T21:00:00Z","report_id":"5c1e09ab","runs":[
 {"spans_dropped":0,"spans":[
  {"span":"invoke_agent","run":"a41f0c22","gen_ai.provider.name":"local","gen_ai.request.model":"Qwen3.8-27B-UD-Q3_K_XL.gguf","context_size":32768,"prompt_template_hash":"9e2d41c0","tools_offered":["read_file","list_dir","search"],"stop_reason":"done"},
  {"span":"chat","seq":1,"gen_ai.usage.input_tokens":4120,"gen_ai.usage.output_tokens":96,"gen_ai.usage.cache_read.input_tokens":0,"context_fill_pct":12,"ttft_ms":410,"duration_ms":2280,"gen_ai.response.finish_reasons":["tool_calls"],"tool_calls_since_user":0,"repeat_4gram_ratio":0},
  {"span":"tool_parse_error","seq":2,"class":"arguments"},
  {"span":"execute_tool","seq":3,"gen_ai.tool.name":"read_file","offered":true,"args":[{"key":"path","len":31,"hmac":"0b7c55e1"}],"retry_index":0,"result":"ok","result_bytes":1840,"duration_ms":3},
  {"span":"execute_tool","seq":4,"gen_ai.tool.name":"web_fetch","offered":false,"result":"not_offered"},
  {"span":"loop","seq":5,"gen_ai.tool.name":"read_file","repeats":3},
  {"span":"condensed","seq":6,"kind":"elide","trigger":"pressure","before":30120,"after":11800},
  {"span":"context_exceeded","seq":7,"reason":"context_ceiling"}]},
 {"spans_dropped":0,"spans":[
  {"span":"invoke_agent","run":"7d03b9e4","gen_ai.provider.name":"api","gen_ai.request.model":"gpt-oss-20b","context_size":131072,"prompt_template_hash":"9e2d41c0","tools_offered":["read_file"],"stop_reason":"aborted_mid_model"},
  {"span":"cancelled","seq":1,"reason":"aborted_mid_model"}]}]}
```

## Run health (item 2q6)

These are what went wrong, said without anyone relaying it. They are sent in
the normal batch, and only when anonymous diagnostics are on. Each is built
**once per run**, when the run stops (and after its progress detectors have
reported), never once per request: a forty-call run adds about seven events
and 2 KiB. `connection.state` and `update` are built once per change.

### `run.summary`

| Field | Type | Meaning |
|---|---|---|
| `inference_calls` | int | model calls in the run |
| `tool_calls` | int | tool calls in the run |
| `max_fill_pct` | int | the fullest the context got, percent of the window |
| `loop` | bool | a `loop` span fired |
| `stop_reason` | string | the run's stop reason |
| `ttft_ms` | int, optional | the first inference's time to first token |
| `reasoning_tokens` | int | reasoning tokens over the run |
| `compactions` | int | compactions in the run |
| `approval_wait_seconds` | int | seconds spent waiting on approval cards |
| `wall_seconds` | int | the run's length |

```json vector:run.summary
{"type":"run.summary","at":"2026-10-04T21:00:05Z","inference_calls":2,"tool_calls":3,"max_fill_pct":41,"loop":true,"stop_reason":"done","ttft_ms":410,"reasoning_tokens":1193,"compactions":1,"approval_wait_seconds":0,"wall_seconds":45}
```

### `model.perf`

One per run. Each of `ttft_ms`, `prompt_ms`, `tokens_per_second`,
`prompt_tokens`, `cached_tokens`, `completion_tokens` and `reasoning_tokens` is
`{"p50","p95","max"}` over the run's model calls; one is absent when the server
never reported it.

| Field | Type | Meaning |
|---|---|---|
| `connection_kind` | string | `local` or `api` |
| `calls` | int | model calls measured |
| `cache_hit_pct` | int | cached prompt tokens as a percent of prompt tokens, over the run |
| `cache_hit_pct_after_compaction` | int, optional | the same, for the first call after each compaction — where the cache drops |
| `reasoning_share_pct` | int | reasoning tokens as a percent of output tokens |

```json vector:model.perf
{"type":"model.perf","at":"2026-10-04T21:00:05Z","connection_kind":"api","calls":21,"cache_hit_pct":53,"cache_hit_pct_after_compaction":21,"reasoning_share_pct":76,"ttft_ms":{"p50":3449,"p95":17724,"max":17928},"prompt_ms":{"p50":3244,"p95":16768,"max":16965},"tokens_per_second":{"p50":46,"p95":47,"max":48},"prompt_tokens":{"p50":17271,"p95":25621,"max":26333},"cached_tokens":{"p50":8391,"p95":18374,"max":18915},"completion_tokens":{"p50":800,"p95":2616,"max":3734},"reasoning_tokens":{"p50":547,"p95":2470,"max":2867}}
```

### `tool.perf`

One per run. `tools` maps each registered tool name used to `calls`, `errors`
(error class → count), `duration_ms` (`{"p50","p95"}`), `size_bucket` of its
largest result (`under_1k`, `under_10k`, `under_100k`, `100k_or_more`), `cut`
(results elided, truncated or too large) and `repeats` (calls with the same
tool and the same arguments as an earlier call in the run).

```json vector:tool.perf
{"type":"tool.perf","at":"2026-10-04T21:00:05Z","tools":{"read_file":{"calls":9,"errors":{"too_large":1},"duration_ms":{"p50":3,"p95":12},"size_bucket":"under_10k","cut":1,"repeats":2}}}
```

### `model.behaviour`

One per run: counts of `empty_replies`, `unparseable_tool_calls`,
`unoffered_tool_calls`, `cut_by_length` (answers that hit the length limit),
`thinking_only_answers` (reasoning and nothing else), and `detectors`: the names
of the progress detectors that fired (`novel_action`, `result_repetition`,
`repeated_timeouts`, `model_says_stuck`, `error_success_ratio`,
`baseline_deviation`).

```json vector:model.behaviour
{"type":"model.behaviour","at":"2026-10-04T21:00:05Z","empty_replies":0,"unparseable_tool_calls":1,"unoffered_tool_calls":0,"cut_by_length":0,"thinking_only_answers":2,"detectors":["result_repetition"]}
```

### `budget.drift`

One per run: `max_pct`, the largest gap between the harness's prompt estimate
and the server's count, as a percent of the server's count.

```json vector:budget.drift
{"type":"budget.drift","at":"2026-10-04T21:00:05Z","max_pct":7}
```

### `compaction`

One per run that compacted: `count`; the last compaction's `kind` and
`trigger`; `before` (the largest) and `after` (the last), in tokens; and
`outcomes`, the summarizer's results by class (`accepted`, `rejected`, `error`).
The bus event of the same name is this aggregate's input and is never sent
itself.

```json vector:compaction
{"type":"compaction","at":"2026-10-04T21:00:05Z","count":45,"kind":"summarize","trigger":"overflow","before":31585,"after":22108,"outcomes":{"accepted":32,"error":34,"rejected":2}}
```

### `approval.wait`

One per approval card answered in the run, at most four: `card_kind`, `seconds`
waited, and `outcome` (`answered`, `denied`, `dismissed`,
`refused_after_10_minutes`). `run.summary` carries the total.

```json vector:approval.wait
{"type":"approval.wait","at":"2026-10-04T21:00:05Z","card_kind":"shell.operator_override","seconds":21409,"outcome":"dismissed"}
```

### `model.refused`

One per distinct refusal in the run, at most four: the HTTP `status`, the
server's own `error_type` word (for example `exceed_context_size_error`),
`connection_kind`, and `count`. Never the server's message.

```json vector:model.refused
{"type":"model.refused","at":"2026-10-04T21:00:05Z","status":400,"error_type":"exceed_context_size_error","connection_kind":"api","count":1}
```

### `connection.state`

One per change of a connection's lamp: `from` and `to` (`ready`, `amber`,
`alarm`, `checking`; `unknown` before the first), and `cause`, the state word
with spaces as underscores. Never which connection.

```json vector:connection.state
{"type":"connection.state","at":"2026-10-04T21:00:05Z","from":"ready","to":"alarm","cause":"unreachable"}
```

### `update`

One per update check, with `check` (`current`, `available`, `error`), and one
per install outcome, with `install` (`ok`, or `failed_` and the phase); both
carry `from` and `to` versions. An instance reports the last install's outcome
once after it starts. The vector shows both fields; an event has one.

```json vector:update
{"type":"update","at":"2026-10-04T21:00:05Z","check":"available","install":"failed_verify","from":"v1.60.11","to":"v1.60.12"}
```

### `settings.shape`

When collection starts (every configuration save) if it changed, and once a
day: `connections`, each with `kind`, `model` (file name), `context_size`,
`reserve`, `reasoning_effort`, `soft_pct` and `summary_pct`; `telemetry` (the
switch); and `os_version`. No address, key, name, path or label.

```json vector:settings.shape
{"type":"settings.shape","at":"2026-10-04T21:00:05Z","connections":[{"kind":"api","model":"Qwen3.8-27B-UD-Q3_K_XL","context_size":32768,"reserve":4096,"reasoning_effort":"medium","soft_pct":0.8,"summary_pct":0.9}],"telemetry":true,"os_version":"windows 10.0.26200"}
```

## The allow-list

These types travel in batches of their own, never beside the older types: a
receiver that does not know them yet refuses their batch (`422
unknown_event_type`) and not the rest. Exactly these leave; any other field is
dropped by the allow-list in `internal/telemetry` before it can.

| Type | Fields |
|---|---|
| `trace` | `report_id`, `runs` |
| `run.summary` | `inference_calls`, `tool_calls`, `max_fill_pct`, `loop`, `stop_reason`, `ttft_ms`, `reasoning_tokens`, `compactions`, `approval_wait_seconds`, `wall_seconds` |
| `model.perf` | `connection_kind`, `calls`, `ttft_ms`, `prompt_ms`, `tokens_per_second`, `prompt_tokens`, `cached_tokens`, `completion_tokens`, `reasoning_tokens`, `cache_hit_pct`, `cache_hit_pct_after_compaction`, `reasoning_share_pct` |
| `tool.perf` | `tools` |
| `model.behaviour` | `empty_replies`, `unparseable_tool_calls`, `unoffered_tool_calls`, `cut_by_length`, `thinking_only_answers`, `detectors` |
| `budget.drift` | `max_pct` |
| `compaction` | `count`, `kind`, `trigger`, `before`, `after`, `outcomes` |
| `approval.wait` | `card_kind`, `seconds`, `outcome` |
| `model.refused` | `status`, `error_type`, `connection_kind`, `count` |
| `connection.state` | `from`, `to`, `cause` |
| `update` | `check`, `install`, `from`, `to` |
| `settings.shape` | `connections`, `telemetry`, `os_version` |
