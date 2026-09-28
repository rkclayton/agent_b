# Diagnostic telemetry — the wire schema

Item 2jg. This document **is** the contract. The allow-list in
`internal/telemetry` is generated against it, and a test fails if the product
grows an event type this document has not classified.

**The receiver is the VPS, not this repository** (item 2lr). Agent_b ships the
sender and the off switch. Nothing here receives anything.

## What is never sent

No content of any kind leaves the machine. Specifically, and without exception:

- No message text, prompt text, model output, reasoning or summary.
- No tool arguments, tool output, file contents or file names.
- No paths, hostnames, URLs, email addresses, credentials or tokens.
- No chat titles, plan text, memory notes or workspace names.
- No session ids, run ids or agent names.

What is sent is **counts, durations, classes and reasons** — the shape of what
happened, never what it was about.

## The envelope

One batch is one JSON object:

```json
{
  "schema": 1,
  "install_id": "3f2a6c1e-0b44-4a1d-9c77-1e5d8a2b4f60",
  "sent_at": "2026-09-26T17:00:00Z",
  "agent_version": "v1.18.0",
  "os": "windows",
  "events": [ … ]
}
```

| Field | Meaning |
|---|---|
| `schema` | this document's version; the receiver rejects what it does not know |
| `install_id` | a random v4 UUID created when telemetry is first enabled, and **regenerated whenever the switch goes off then on again**. It identifies an install, never a person, and a new one cannot be joined to the old. |
| `sent_at` | when the batch left, RFC 3339 UTC |
| `agent_version` | the release tag |
| `os` | `windows` |

## The events

Each event is `{"type": …, "at": …, <fields>}`. `at` is RFC 3339 UTC truncated
to the second.

### `run.stopped`

| Field | Type | Meaning |
|---|---|---|
| `reason` | string | one of the fixed stop reasons (item 2iw); never free text |
| `turns` | int | |
| `total_ms`, `model_ms`, `tool_ms`, `waiting_ms`, `compaction_ms` | int | item 2ji's buckets |
| `prompt_ms`, `generation_ms` | int, **optional** | absent when the server reported no timings — **absent, never zero** |
| `retries`, `compactions`, `empty_replies`, `repeated_calls` | int | |
| `model_calls`, `tool_calls` | int | how many model calls the run made and how many tool results came back — the two counts the reflection floor reads, and nothing about what any of them were |
| `model_class` | string | `local` or `remote`; never the model name, endpoint or connection id |

### `tool.result`

| Field | Type | Meaning |
|---|---|---|
| `name` | string | the tool's registered name, from the fixed list of thirteen |
| `ok` | bool | |
| `ms` | int | |
| `class` | string, optional | present only when `ok` is false: the error's class, from the fixed list below. Never the error text. |

Error classes: `not_found`, `permission`, `refused_by_guard`, `timeout`,
`network`, `parse`, `too_large`, `cancelled`, `bad_request`, `unavailable`,
`internal`.

### `error`

| Field | Type | Meaning |
|---|---|---|
| `where` | string | the fixed subsystem label already on the event |
| `class` | string | as above |

## Everything else is dropped

Every other event type the product emits is classified **dropped**, and the
classification is explicit rather than implied by absence. The classification
test in `internal/telemetry` fails if the product emits a type this document has
not named in either list.

Dropped: `model.delta`, `model.progress`, `model.request`, `model.response`,
`model.retry`, `model.unreachable`, `model.reachable`, `model.busy`,
`delegate.usage`, `stage`, `budget`, `message.appended`, `message.updated`,
`message.removed`, `message.queued`, `messages.reminted`, `tool.call`,
`tool.toggled`, `compaction`, `compaction.summary`, `session.created`,
`session.closed`, `session.reopened`, `session.renamed`, `session.reset`,
`session.updated`, `session.restore_failed`, `chat.named`, `chat.exported`,
`run.queued`, `run.started`, `run.stopping`, `run.resumed`, `run.labeled`,
`run.aborted`, `approval.required`, `approval.decided`, `cycle.detected`,
`policy.approved`, `policy.denied`, `policy.revoked`, `workspace.conflict`,
`workspace.bound`, `memory.noted`, `memory.cleared`, `memory.flushed`,
`project.instructions_loaded`, `connection.probed`, `connection.discovering`, `probe.request`,
`config.changed`, `shell.identity`, `shell.credential`, `shell.grant`,
`shell.grant_lapsed`, `service.identity_unavailable`, `file.grant`,
`file.grant_lapsed`, `signing.applied`, `operator.context`, `ui.error`,
`subscriber.dropped`, `log.retention`, `item.done`, `item.stuck`, `plan.done`,
`c.job`, `stats.cleared`, `files.delivered`, `navigation.started`,
`navigation.measured`, `navigation.suppressed`, `navigation.document_started`,
`navigation.document_completed`, `notification.failed`, `notification.changed`,
`update.changed`, `progress.shadow`, `progress.aux`, `speech`,
`agent.connection_change`, `reflection.skipped`, and `projection.patch` and `snapshot` — which carry
the whole conversation as a client reads it, and are the two most important
names on this list.

## Redaction

Applied to every string field before it leaves, as a belt against a class name
or reason that turns out to carry more than it should:

| Pattern | Becomes |
|---|---|
| an absolute path | `<path>` |
| a URL | `<host>` |
| a bare hostname or IP | `<host>` |
| an email address | `<email>` |
| a long token-shaped run of characters | `<token>` |

Any remaining string is cut to 200 characters.

## Sending

- A batch leaves every **5 minutes**, or as soon as **200 events** have
  accumulated, whichever comes first.
- A batch is at most **64 KB**. A batch that would exceed it is split.
- When the endpoint is unreachable the batch is queued on disk under the data
  root. The backlog is bounded to **24 hours**; older batches are dropped oldest
  first, with a log line saying how many.
- The endpoint comes from configuration. **There is no default endpoint**: until
  one is set, nothing is collected and nothing is queued, because there is
  nowhere for it to go.

## Off means off

The switch is in **Settings → About**: *Send diagnostic telemetry*, on by
default.

Off does not mean "sends less". Off means:

- the event-bus subscriber is **detached**, so nothing is collected;
- the on-disk queue is **deleted**;
- the sender is **not running**.

This is tested as an absence, not as a flag: with the switch off, the bus has no
telemetry subscriber, the queue directory does not exist, and no batch is ever
handed to the transport.

Turning it back on issues a **new** install id.

## What was sent

Settings → About lists the last 20 batches exactly as they left the machine, so
the claim above can be checked rather than believed.
