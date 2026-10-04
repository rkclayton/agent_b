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

## `run.summary`

One per run, in the normal batch, only when anonymous diagnostics are on.

| Field | Type | Meaning |
|---|---|---|
| `inference_calls` | int | model calls in the run |
| `tool_calls` | int | tool calls in the run |
| `max_fill_pct` | int | the fullest the context got, percent of the window |
| `loop` | bool | a `loop` span fired |
| `stop_reason` | string | the run's stop reason |
| `ttft_ms` | int, optional | the first inference's time to first token |

```json vector:run.summary
{"type":"run.summary","at":"2026-10-04T21:00:05Z","inference_calls":2,"tool_calls":3,"max_fill_pct":41,"loop":true,"stop_reason":"done","ttft_ms":410}
```

## The allow-list

These types travel in batches of their own, never beside the older types: a
receiver that does not know them yet refuses their batch (`422
unknown_event_type`) and not the rest. Exactly these leave; any other field is dropped by the allow-list in
`internal/telemetry` before it can.

| Type | Fields |
|---|---|
| `trace` | `report_id`, `runs` |
| `run.summary` | `inference_calls`, `tool_calls`, `max_fill_pct`, `loop`, `stop_reason`, `ttft_ms` |
