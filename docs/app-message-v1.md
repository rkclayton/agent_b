# The application message — `agentb-app-message-v1`

Version: **1**. Every unit carries `"v": 1` as its first field. This version is independent of the
broker's wire version: the broker's `protocol-v0.md` states that the `CIPHERTEXT` payload is an
opaque application payload "whose separately versioned format belongs to the Windows repository",
and this is that format. A wire change does not change this number and a change here does not
change the wire's.

This document does not define a field. **`INTERFACES.md` is the authority for every field carried
here**, and this document says only how those existing shapes are framed for a socket that carries
ciphertext instead of HTTP. Where the two disagree, `INTERFACES.md` wins and this document is wrong.

Normative vectors: `internal/appmessage/testdata/vectors-v1.json`. The gate is
`internal/appmessage/vectors_test.go`. An implementation in another repository verifies against
those bytes, not against this prose.

## What sits above and below

Below: one `CIPHERTEXT` frame per broker message, sealed under the pairing's traffic key with AAD
`version || sender_key_id || recipient_key_id || message_id || sequence`. **The payload ceiling is
8 MiB including JSON encoding**, control payloads are capped at 64 KiB, and a receiver applies the
limit before allocation. The broker forwards the frame unchanged and never parses it. A recipient
sends `ACK` only after the AEAD opens and the content is accepted.

Above: the desktop API exactly as `INTERFACES.md` defines it. There is no second protocol.

The plaintext of one `CIPHERTEXT` payload is one UTF-8 JSON object — a **unit** — with no framing
bytes of its own.

## The unit budget

`UNIT_MAX = 8 388 608` bytes, the broker's ceiling. A sender MUST NOT emit a unit whose UTF-8
encoding exceeds `UNIT_MAX`. Because the ceiling is stated to include JSON encoding, a sender
measures the encoded bytes it is about to seal, not the size of the value it started from.

Measured, 2026-09-27, on the operator's own data root: the largest projected snapshot across 32
chats is **474 248 bytes** (175 messages), from a journal of 44 422 091 bytes. A snapshot is a
projection of a journal and is roughly two orders of magnitude smaller than it, so the oversize case
is not reached by today's largest chat. It is specified anyway, because the ceiling is real, one
retained tool result can approach it, and an implementation that has never split will split wrongly
the first time it must.

## Downstream: desktop → device

| `kind` | Carries | `INTERFACES.md` authority |
|---|---|---|
| `snapshot` | the SSE `snapshot` payload for one session, unchanged | "Event envelope and transport", `SessionSnapshot` schema 1 |
| `patch` | one `projection.patch` envelope, unchanged | "Event envelope and transport" |
| `event` | one global durable event envelope, unchanged | "Event envelope and transport" |
| `response` | the single answer to one upstream `request` | "HTTP API" |
| `part` | one ordered fragment of an oversize unit of any kind above | this document only |

```
{"v":1,"kind":"snapshot","session_id":"<id>","data":{ … SessionSnapshot … }}
{"v":1,"kind":"patch","data":{ … projection.patch … }}
{"v":1,"kind":"event","data":{ … {seq,ts,session_id,run_id,type,data} … }}
```

`data` is the envelope the tailnet client already receives, byte-for-byte after JSON re-encoding.
A device's view through the broker is therefore identical to its view over the tailnet, route by
route: the same `snapshot` on connect, the same `projection.patch` stream after it, the same cursor
semantics, the same `projection_stale` and `complete` flags.

Each chat snapshot also carries `folder` (the slash-separated path below the PC's Chats root, or
`""` at the root) and `last_activity` (RFC3339Nano, the time its newest projected chat entry was
added). The join begins with one global event whose type is `chat.list.snapshot` and whose data is
`{folders:[path...]}`. Later PC folder changes use one constant-size global event of type
`chat.list.patch`: `{operation:"add",path}`, `{operation:"rename",path,value}`,
`{operation:"delete",path}`, `{operation:"move",session_id,folder}`, or
`{operation:"activity",session_id,last_activity}`. A rename's `value` is the
new full folder path. These list events use the existing `event` unit and do not alter a chat's
projection cursor.

## Splitting a unit that does not fit

A unit whose encoding would exceed `UNIT_MAX` is sent as an ordered sequence of `part` units:

```
{"v":1,"kind":"part","unit_id":"<32 lower-case hex>","index":0,"count":3,
 "total_bytes":25165824,"sha256":"<64 lower-case hex>","chunk":"<base64url, unpadded>"}
```

- `unit_id` is 16 random bytes as 32 lower-case hex, fresh per split unit, matching the broker's
  identifier convention.
- `index` is zero-based and MUST arrive in increasing order on the connection. `count` is the total
  number of parts and is identical in every part of the unit.
- `chunk` is unpadded base64url over a contiguous slice of the **UTF-8 bytes of the whole unit**.
  A split may land mid-codepoint; a receiver concatenates bytes and decodes once, at the end.
- `total_bytes` is the length of the whole unit's UTF-8 encoding and `sha256` is its digest, both
  identical in every part.
- Every `part` unit, encoded, MUST itself fit `UNIT_MAX`.

A receiver holds at most one incomplete unit per sender. It reassembles in `index` order, checks the
reassembled length against `total_bytes` and its digest against `sha256`, and only then decodes and
applies. A mismatch is treated as a gap.

**Resume after a gap uses the cursor the snapshot already carries.** A receiver that misses a part,
sees `index` out of order, or fails the digest **discards the whole partial unit and does not apply
any of it**, then asks for a full resync. It does not request the missing part: the projection's own
rule is that `previous_cursor` mismatch causes a full snapshot resync, and a partial unit is exactly
that condition arriving in fragments. This matches the iOS reducer, which already stops on a gap by
design rather than applying a patch out of order; the rule is written for that behaviour, not around
it. Resolution of the open question in the item: **the split needs no sequence of its own beyond
`index`**, because recovery is always "discard and resync by cursor", never "repair in place".

A `snapshot` unit is never resumed mid-flight. Resync means a new `snapshot` from the current
cursor.

The split rule is parameterised by the budget, not by the constant: a sender splits against whatever
`UNIT_MAX` is in force. The committed vectors therefore declare a reduced `unit_max` so the rule is
exercised by a fixture small enough to read, and the reduced budget is the only difference between
the vector and production.

## Canonical encoding

The vectors carry exact bytes, so the encoding must be reproducible. **Canonical form is compact
JSON — no insignificant whitespace — with object keys sorted by Unicode code point**, which is what
Go's `encoding/json` produces for a map. The examples above are written in reading order for a human;
the bytes on the wire are the canonical form. A receiver MUST NOT depend on key order.

## Upstream: device → desktop

One shape, keyed by the route it stands for:

```
{"v":1,"kind":"request","id":"<32 lower-case hex>","route":"message","body":{ … }}
```

`id` is 16 random bytes as 32 lower-case hex, unique per request on the connection. `route` is one
of the names in the table below — **not** a URL path, so a device cannot compose a path the desktop
did not publish. `body` is the request body `INTERFACES.md` defines for that route.

| `route` | HTTP route it stands for | `body` |
|---|---|---|
| `message` | `POST /api/message` | `{session_id,text,attachments?}` |
| `stop` | `POST /api/stop` | `{session_id?,all?}` |
| `approve` | `POST /api/approve` | `{session_id,call_id,decision}` |
| `tool` | `POST /api/tools/{name}` | `{name,session_id,enabled}` |
| `state` | `GET /api/state` | `{}` |
| `resync` | `GET /api/state` for one session | `{session_id}` |
| `chat.create` | `POST /api/sessions` | `{label?,agent_id?,connection_id?}` — connection selection added 2026-09-30; agent selection added 2026-10-09 |
| `chat.history` | `GET /api/sessions/{session_id}/history` | `{session_id,before?}` — 50 older entries at a time |
| `chat.mirror` | append one owner's journal event to its mirror | `MirrorAppend` — added 2026-09-30 |
| `chat.mirror.since` | ask a mirror for its durable per-chat cursor | `{chat_id}` — added 2026-09-30 |
| `chat.mirror.take` | transfer ownership to the requesting peer | `{chat_id,after_seq}` — added 2026-09-30 |
| `chat.rename` | `POST /api/sessions/{session_id}` | `{session_id,label}` — added 2026-10-05 |
| `chat.delete` | `DELETE /api/sessions/{session_id}` | `{session_id}` — added 2026-10-05 |

`tool` carries the path segment as a `name` field for the same reason: the route set is closed.

`state.connections` is the PC's ordered Connection sheet. Each entry is exactly
`{id,label,model,host,vision,docs,tools,ctx}`: `host` is only the URL host (and optional port),
`vision` is the probed vision class, `docs` and `tools` are booleans, and `ctx` is the effective
context-token count. It carries no URL path, credential, API key, system prompt, sampling value or
probe finding.

`chat.create` carries an optional `label`, `agent_id`, and legacy `connection_id`, and NOTHING ELSE. State lists safe agent rows `{id,name,connection_id,model}` beside connections. A named agent creates the chat on it. A released client naming a connection maps to `agent_b` when it uses that connection, otherwise the first matching agent. With neither id, the desktop uses `agent_b`. A known id creates the
same ordinary PC chat as choosing that agent in the top strip. An unknown id returns the ordinary
`400` field refusal and creates nothing. It still cannot carry `role` or
`source_session_id`, so a device cannot open a planner or worker chat. With no `label`, the desktop
names the chat as it names any unlabelled one.

`chat.history` carries a session id and an optional exclusive `before` index. It returns at most
50 chat entries plus `{start,before,total}`. Omitting `before` returns the newest page; passing the
returned `start` walks toward the first entry without transferring the rest of the retained store.

`chat.rename` and `chat.delete` call the same handlers as the PC. Rename therefore applies the
same label validation and retained-folder rename. Delete performs 2py's one act: it stops a running
chat if necessary, closes an open one, permanently deletes it, and emits the ordinary
`chat.deleted` event. Neither route accepts a path, folder, role, connection, or other lifecycle
choice.

## Mirrored chats

The three `chat.mirror` routes are the one bidirectional exception to the upstream heading: either
peer may send them, and the receiver owes the ordinary single `response`. All older routes remain
device → desktop. A chat has one `origin` (`phone` or `pc`), one `owner` (`phone` or `pc`), and one
append sequence shared across ownership transfers. The peer named by `owner` is the only peer that
may append. The other peer is a read-only mirror.

`chat.mirror` carries exactly one append:

```
{"chat_id":"<stable id>","origin":"phone","owner":"phone","seq":4,
 "event":{"ts":"<RFC3339>","run_id":"r1","type":"message.appended","data":{…}},
 "files":[{"path":"attachments/photo.jpg","bytes":1234,"sha256":"<64 lower-case hex>",
           "content":"<base64url, unpadded>"},
          {"path":"attachments/photo.jpg.txt","bytes":321,"sha256":"<64 lower-case hex>",
           "content":"<base64url, unpadded>"}]}
```

This body is `MirrorAppend`. `files` is optional and is present only on the first event that names
those files. It carries an attachment and its extraction sidecar as separate entries. Each `path`
is a canonical direct child of `attachments/`, each digest and byte count MUST match the decoded
content, and a receiver writes neither file until every entry validates. The event deliberately
omits `seq` and `session_id`: `seq` is this append's outer sequence, and the receiver assigns its
own durable event sequence and the `chat_id`. `body` and `raw` diagnostic fields are not carried.

Binary bytes do not introduce another framing rule. The sender base64url-encodes them in `files`,
canonical-encodes the complete request unit, and applies the existing `part` rule when that unit
exceeds `UNIT_MAX`. Thus an attachment and sidecar can span any number of `part` units, but nothing
is decoded, written or journalled until the whole request has passed the part count, length and
SHA-256 checks. A gap discards the whole partial request and recovery starts at `chat.mirror.since`.

The first accepted append for a chat MUST be sequence 1 and a `session.created` seed. The receiver
stores `origin` and `owner` with that chat and appends the event to the same retained journal shape
as a local chat. Later sequences are accepted only from the current owner and only at the next
number. Success is `200 {chat_id,seq}`. Repeating the last accepted sequence with byte-identical
content is an idempotent success; reusing a sequence with different content, sending a gap, or
appending as the non-owner is `409`, with `body.expected_seq`. A non-owner append is also logged
locally as a refusal.

`chat.mirror.since {chat_id}` returns `200 {chat_id,last_seq,origin,owner}`. An owner calls it after
connecting and sends its durable appends from `last_seq + 1`; the mirror applies them strictly in
order. Until that drain reaches the owner's current sequence, the mirror exposes only the durable
prefix it has. The owner retains unsent appends across disconnects, so reconnect never depends on
an in-memory queue.

`chat.mirror.take {chat_id,after_seq}` is sent by the mirror to the owner. `after_seq` MUST equal the
owner's current sequence, which prevents ownership moving to a peer that is missing an append. The
owner first durably changes `owner`, then answers `200 {chat_id,owner,next_seq}`; only that response
makes the requester the writer, starting at `next_seq`. A retry after a lost response is idempotent.
If the owner's run is not idle it answers `409 {error:"the phone is mid-turn"}` when the phone owns
the chat (and the corresponding `pc` wording when the PC owns it). No event from the old owner is
accepted after the transfer. These rules make concurrent writers impossible rather than reconciled.

**Version stays 1.** A device that does not know a route never tunnels it and is answered `501` by
the rule above, so ADDING a route is compatible by this document's own terms: an older device
behaves exactly as it did, and a newer one gets an answer only from a desktop that published the
route. Each added route carries the date it was added, in the table.

The mutation token of `INTERFACES.md`'s "HTTP API" is a per-launch browser/CSRF secret and is **not**
carried here. Authority on this transport is the pairing, established by the broker's pairing flow
and bounded by revocation; a device never learns the mutation token and never sends one.

## The return path

**Every `request` receives exactly one `response` naming its `id`, and nothing else.**

```
{"v":1,"kind":"response","id":"<the request's id>","status":202,"body":{"run_id":"r1"}}
```

- `status` is the HTTP status the route would have returned over the tailnet: `200`, `201`, `202`,
  `400`, `404`, `409`, `413`, `501`. The device's state machine is therefore the one it already has.
- `body` is the response body for that status, or absent when the route returns none.
- A refusal is a `response`, not an error frame:
  `{"v":1,"kind":"response","id":"…","status":409,"body":{"error":"a run is already active"}}`.
- A request the desktop cannot parse at all, or whose `route` is not in the table, is answered
  `status: 501` with `body.error` naming the unsupported route. **It is never tunnelled.**
- A device MUST NOT wait on a request with no `response`. A desktop that refuses a request before
  reaching its handler still answers, so a phone never waits on something the desktop dropped.

A `response` may be split by the `part` rule like any other downstream unit.

## What a device may NOT reach through the broker

A broker-paired phone has full desktop authority by design (`INTERFACES.md`, "Phone pairing and
application messages"), which makes the list of what it cannot reach a security statement rather than a convenience.
**The route table above is exhaustive.** Anything else is answered `501` and is not proxied,
including, explicitly:

- `POST /api/config`, `/api/service-account`, `/api/hardening`, `/api/shell-credential` — machine
  state and credentials. The OS boundary is not reachable from a paired phone.
- `shell.operator_context` and `shell.service_account`, which `INTERFACES.md` already requires to
  come from a loopback request whose client process carries the operator's Windows SID. A broker
  frame is not such a request and cannot become one.
- `POST /api/update` — no install is startable from a device.
- `POST /api/host-window`, `/api/plans`, `/api/plan/accept`, `/api/plan/marker`, `/api/plan/go`,
  every session-lifecycle route EXCEPT the creation, rename, and delete routes named above — close,
  reopen, move, archive and the rest remain refused — `/api/notifications`,
  `/api/operator-files`, `/api/workspaces` and its policy routes, `/api/standing-grants`.
- Anything reached by a path rather than a name: there is no generic passthrough, and `route` is
  matched against the closed set above by exact string equality.

A stolen paired phone can therefore send a message, stop a run, answer an approval card, toggle a
tool for the next request, read state, **create a chat**, mirror its own chats and their attachment
bytes onto this machine, and request ownership of an already mirrored chat. It cannot change the
  machine, install anything, register a plan, close, move or archive a chat, or reach a credential.

**The exposure `chat.create` adds, stated plainly:** a stolen paired phone can see the safe
Connection sheet and open empty chats, as many as it likes, on any listed connection. That can incur
model cost, costs disk and clutters the chat list,
and it is visible — every one appears on the desktop like any other chat. It reaches no new data:
a new chat starts empty, and reading anything still requires `state` or `resync`, which the phone
already had. Revocation remains the answer, and it is immediate.

**The exposure `chat.rename` and `chat.delete` add, stated plainly:** a stolen paired phone can
rename or permanently delete any PC chat it can identify, with exactly the desktop's effects. It
still cannot choose a folder or exercise another lifecycle action. Revocation remains immediate.

## What the desktop does today (item 2o7)

While a pairing exists, the desktop holds one broker session and answers every `request` through
the same handlers the page uses; on each connection the device receives a `snapshot` of every chat,
then `patch` and `event` units, split into `part` units over the budget. The pairing and the
desktop's identity live in memory only, so a restart of Agent_b ends the pairing and the phone
pairs again.

Not yet: **pushes**. The live broker refuses the sealed push frame the desktop builds as
`malformed`, and that refusal ends the whole session, so no push is sent until the frame's shape is
confirmed with the broker repository.
