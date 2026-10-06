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

## `trace` — a struggling run

With anonymous diagnostics on, one trace is sent automatically when a run
exhausts context, fires a detector, records two tool errors, calls an unoffered
tool, is refused by the model server, or is stopped by the person. Clean runs
send none. With diagnostics off, no trace leaves. At most six traces leave in a
rolling hour. Each trace is at most **48 KiB** and carries that run only.

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

### `header.state` and `import.hermes`

These change-boundary events contain no displayed or imported text. `header.state`
has `state` (`named`, `not_runnable`, or `no_chat`). Its reason_code values are
`none`, `missing_reason`, `no_connection`, `missing_connection`, `missing_workspace`,
`workspace_unavailable`, `missing_endpoint`, `missing_model`, and `other`.
`reason_code` is `none` exactly when `state` is `named` or `no_chat`.
`import.hermes` has integer `skills`, `memory_files`, and `secret_names` counts,
plus `result` (`ok`, `refused`, or `failed`).

```json vector:header.state
{"type":"header.state","at":"2026-10-06T15:59:58Z","state":"named","reason_code":"none"}
```
```json vector:header.state
{"type":"header.state","at":"2026-10-06T16:00:00Z","state":"not_runnable","reason_code":"missing_connection"}
```
```json vector:header.state
{"type":"header.state","at":"2026-10-06T16:00:01Z","state":"no_chat","reason_code":"none"}
```
```json vector:import.hermes
{"type":"import.hermes","at":"2026-10-06T16:00:02Z","skills":2,"memory_files":2,"secret_names":2,"result":"ok"}
```

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

Error counts use the closed classes `exit_nonzero`, `invalid_args`, `not_found`,
`timeout`, `denied`, `too_large`, and `internal`.

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

## The app around the runs (item 2q7)

How the app itself is doing: start-up, the page, the phone link, installs,
resources and what gets used. Gathered for an hour and sent as one event of
each type (`app.start` once per process, `install` once per outcome, at most
four an hour), only when anonymous diagnostics are on. **Every size and count
that could single out a machine is a power-of-two bucket**: the smallest power
of two at or above the value, so `chats: 64` means 33 to 64 chats.

### `app.start`

Once per process, in the first hour.

| Field | Type | Meaning |
|---|---|---|
| `listen_ms` | int | from process start to the port answering |
| `window_ms` | int, optional | to the host window being shown; absent for a background start |
| `first_answer_ms` | int, optional | to the first model answer; absent when none came in the hour |
| `previous_exit` | string | how the last instance ended: `clean`, `crash` (a crash record was left), `unclean` (it ended without recording a reason) |

```json vector:app.start
{"type":"app.start","at":"2026-10-04T21:00:05Z","listen_ms":851,"window_ms":1420,"first_answer_ms":5230,"previous_exit":"clean"}
```

### `app.lifecycle`

One content-free event when the serving process starts and one when it observes
its own exit. A process killed without an observable exit is reported by the
next start as `previous_exit: "unrecorded"`.

| Field | Type | Meaning |
|---|---|---|
| `phase` | `start` or `exit` | which lifecycle edge this event records |
| `previous_exit` | string, start only | `user`, `installer`, `session_end`, `crash`, `killed`, `unknown`, or `unrecorded` |
| `cause` | string, exit only | `user`, `installer`, `session_end`, `crash`, `killed`, or `unknown` |
| `uptime_s` | int, exit only | whole seconds this process served |

```json vector:app.lifecycle
{"type":"app.lifecycle","at":"2026-10-06T14:02:08Z","phase":"exit","cause":"installer","uptime_s":123}
```

### `page.health`

| Field | Type | Meaning |
|---|---|---|
| `state_bytes` | int | the largest state answer the page was sent, bucketed |
| `state_ms` | object, optional | `{"p50","p95"}` of state answer time |
| `page_load_ms` | object, optional | `{"p50","p95"}` of in-app navigations, click to painted |
| `longest_freeze_ms` | int | the longest task that held the page |
| `js_errors` | array | per JavaScript error kind, at most 16: `name` (the error's own name, e.g. `TypeError`, or `unnamed`), `file` and `line` in OUR code (`shell.js`, 412; empty and 0 when not ours), `count`. Never a message or a value. |

```json vector:page.health
{"type":"page.health","at":"2026-10-04T21:00:05Z","state_bytes":33554432,"state_ms":{"p50":180,"p95":2400},"page_load_ms":{"p50":120,"p95":900},"longest_freeze_ms":640,"js_errors":[{"name":"TypeError","file":"shell.js","line":412,"count":3}]}
```

### `link.health`

The phone link (the broker session), when it did anything in the hour.

| Field | Type | Meaning |
|---|---|---|
| `connects` | int | sessions established |
| `drops` | object | drops by reason class: `eof`, `timeout`, `reset`, `refused`, `closed`, `other` |
| `reconnect_ms` | object, optional | `{"p50","p95"}` from a drop to the next connect |
| `refused` | object | frames the broker refused, by its rule code |
| `pushes` | object | push sends by the broker's answer (`accepted`, `no_token`, `refused`) |
| `join_bytes` | int | the most one phone join sent, bucketed |

PC-to-phone message latency is not measured: the link keeps no send time.

```json vector:link.health
{"type":"link.health","at":"2026-10-04T21:00:05Z","connects":3,"drops":{"eof":2},"reconnect_ms":{"p50":1200,"p95":4100},"refused":{"queue_full":1},"pushes":{"accepted":4},"join_bytes":67108864}
```

### `install`

One per installer or updater outcome: `step` (the phase it reached or failed
at), `class` (`ok` or `failed`), `from` and `to` versions.

```json vector:install
{"type":"install","at":"2026-10-04T21:00:05Z","step":"verify","class":"failed","from":"v1.60.12","to":"v1.60.13"}
```

### `resource`

| Field | Type | Meaning |
|---|---|---|
| `memory_peak_bytes` | int | the process's peak memory from the runtime, sampled each minute, bucketed |
| `data_bytes` | int, optional | the data folder's size, bucketed |
| `chats_bytes` | int, optional | the chat store's size, bucketed |
| `chats` | int, optional | how many chats, bucketed |
| `ram_bytes` | int, optional | installed memory, bucketed |
| `arch` | string | CPU architecture (`amd64`, `arm64`) |
| `os_version` | string, optional | OS name and build numbers |

```json vector:resource
{"type":"resource","at":"2026-10-04T21:00:05Z","memory_peak_bytes":268435456,"data_bytes":4294967296,"chats_bytes":1073741824,"chats":64,"ram_bytes":34359738368,"arch":"amd64","os_version":"windows 10.0.26200"}
```

### `feature.use`

Counts for the hour: `chats_created`, `messages_sent`, `voice` (voice turns);
and objects of counts: `tools` by registered
tool name, `attachments` by kind (`text`, `office`, `pdf`, `image`, `zip`,
`binary`), `settings_pages` by page id, `approvals` by card kind and answer.

```json vector:feature.use
{"type":"feature.use","at":"2026-10-04T21:00:05Z","chats_created":2,"messages_sent":14,"voice":0,"tools":{"read_file":22,"search":5},"attachments":{"image":1},"settings_pages":{"connections":2,"about":1},"approvals":{"shell.operator_override.folder":1}}
```

## On-device (iOS) (item 2q8)

These additions let the phone use this same schema; they do not change PC
behaviour. They contain no free text, and any identifier is at most eight
characters. All named sets and object keys below are closed.

### (a) `chat`

When `gen_ai.provider.name` is `on_device`, a chat span may add context and
component sizes (`ondevice.context_size`, `instructions`, `tool_schemas`,
`transcript`, `prompt`) in `ondevice.size_unit` (`tokens` or `chars`); the
`ondevice.slots` object gives `goal`, `facts`, `notes`, `recent`, and `recall`
sizes. It also adds boolean `ondevice.schema_in_prompt` and
`ondevice.prewarmed`, integer `ondevice.model_load_ms`, numeric `tok_s`, and
optional `error.type`: `exceeded_context_window`, `guardrail_violation`,
`decoding_failure`, `unsupported_language`, `assets_unavailable`,
`rate_limited`, `concurrent_requests`, `refusal`, or `other`.

```json vector:ondevice.chat
{"span":"chat","gen_ai.provider.name":"on_device","ondevice.context_size":4096,"ondevice.size_unit":"tokens","ondevice.instructions":512,"ondevice.tool_schemas":1024,"ondevice.transcript":2048,"ondevice.prompt":4096,"ondevice.slots":{"goal":256,"facts":256,"notes":128,"recent":1024,"recall":256},"ondevice.schema_in_prompt":true,"ondevice.prewarmed":true,"ondevice.model_load_ms":1024,"tok_s":16,"error.type":"decoding_failure"}
```

### (b) `invoke_agent`

An `invoke_agent` span may give `ondevice.availability`: `available`,
`device_not_eligible`, `apple_intelligence_not_enabled`, `model_not_ready`, or
`other`.

```json vector:ondevice.invoke
{"span":"invoke_agent","run":"a41f0c22","gen_ai.provider.name":"on_device","ondevice.availability":"available"}
```

### (c) `condensed`

A condensed span may use `unit: entries` (as well as `tokens` or `chars`).

```json vector:ondevice.condensed
{"span":"condensed","seq":6,"kind":"elide","trigger":"pressure","before":64,"after":32,"unit":"entries"}
```

### (d) `ondevice.run`

One aggregate per run gives `availability` from (b); `errors` counts keyed by
(a)'s error set; `prewarm` `hit`/`miss` counts; and `routes` counts keyed only
by `on_device`, `pc_link`, `api`, or `other`. `pictures` has at most the 72
closed combinations of `outcome` (`made`, `failed`, `declined`), `cause`
(`none`, `unsupported_style`, `guardrail`, `unavailable`, `timeout`, `other`),
and `style` (`illustration`, `animation`, `sketch`, `other`), plus `count`.

```json vector:ondevice.run
{"type":"ondevice.run","at":"2026-10-04T21:00:05Z","availability":"available","errors":{"decoding_failure":1},"pictures":[{"outcome":"made","cause":"none","style":"illustration","count":1}],"prewarm":{"hit":2,"miss":1},"routes":{"on_device":2,"pc_link":1,"api":0,"other":0},"sizes":{"context_size":4096,"prompt":4096}}
```

### (e) Aggregate sizes

Every `sizes` value is rounded up to a power-of-two bucket. Its closed keys
are `context_size`, `instructions`, `tool_schemas`, `transcript`, `prompt`,
`goal`, `facts`, `notes`, `recent`, and `recall`.

```json vector:ondevice.sizes
{"type":"ondevice.run","at":"2026-10-04T21:00:05Z","sizes":{"context_size":4096,"instructions":512,"tool_schemas":1024,"transcript":2048,"prompt":4096,"goal":256,"facts":256,"notes":128,"recent":1024,"recall":256}}
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
| `app.start` | `listen_ms`, `window_ms`, `first_answer_ms`, `previous_exit` |
| `app.lifecycle` | `phase`, `previous_exit`, `cause`, `uptime_s` |
| `page.health` | `state_bytes`, `state_ms`, `page_load_ms`, `longest_freeze_ms`, `js_errors` |
| `link.health` | `connects`, `drops`, `reconnect_ms`, `refused`, `pushes`, `join_bytes` |
| `install` | `step`, `class`, `from`, `to` |
| `resource` | `memory_peak_bytes`, `data_bytes`, `chats_bytes`, `chats`, `ram_bytes`, `arch`, `os_version` |
| `feature.use` | `chats_created`, `messages_sent`, `tools`, `attachments`, `voice`, `settings_pages`, `approvals` |
| `ondevice.run` | `availability`, `errors`, `pictures`, `prewarm`, `routes`, `sizes` |
