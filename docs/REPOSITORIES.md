# Where the work lives

Item 2lr. One place a reader can consult to say where a new piece of work belongs
without asking.

**Nothing has moved.** This document is the decision and the map; a move is its
own item.

## Four repositories, one deliverable each

| Repository | Owns | Today |
|---|---|---|
| **vps** | the broker, the telemetry receiver, and whatever serves releases | not yet created; the broker's contract is item 2kq |
| **windows** | this repository — the harness and its installer | exists, and is what you are reading |
| **mac** | the Apple client | exists already, on the Mac, reached from this machine over a share |
| **android** | the Android client | when it starts |

**Nothing is shared by copying.** What more than one of them needs is a
*published artifact*, not a file two repositories both edit. See *What crosses a
boundary* below.

Each repository gets a `plan.md` and an append-only `NOTES.md` on the same
discipline. Only this one carries the full item catalogue; four plans of this
size is not what one deliverable each means.

## The VPS has three jobs, and they are one service's worth of work

1. **Routing**, under item 2kq — the broker between a phone and a machine running
   Agent_b.
2. **Receiving telemetry**, under item 2jg. See *Telemetry lands there, not here*.
3. **Serving what a release needs** — an update manifest a client can ask for,
   the assets it then fetches, and the transport 2kq already provides.

These are three jobs of one service, not three products.

### What the VPS cannot do

**It cannot build an Apple client.** A signed iOS build needs macOS and Xcode.
That is the Mac's job, and it is the constraint the Apple client's own plan
already carries. This decision does not pretend otherwise.

What the VPS *can* do for a release: hold what a release is fetched **from**,
build and sign an **Android** client, and take a **Mac's upload**.

## Telemetry lands there, not here

Item 2jg's receiver is the VPS. This repository keeps **only the sender and the
off switch**.

That is not an accident of sequencing — it is what keeps "off means off" a
property of the client. A receiver on a user's machine would be a service on a
user's machine, and there is no reason for one. Item 2ji's carded telemetry half
is satisfied by the VPS having a receiver, not by this repository growing one.

## What crosses a boundary is versioned

The wire between a client and the VPS is the same kind of artifact as item 2lo's
projection tapes: **described once, emitted by the side that owns it, and
consumed by the others as data.**

A client and the VPS are never assumed to have been built together. The tapes in
`internal/projection/testdata/tapes/` are the worked example — this repository
owns the projector, emits the tapes, and the Apple and Android clients consume
them as data with no tooling from here.

## What in this repository would belong elsewhere

Surveyed at rel-1.18.0/W0 and **named without moving**, because a move is its own
item.

| | Verdict |
|---|---|
| `serve/` — 18 files of llama.cpp start scripts and reliability probes | **Has no home in the four.** It is model-server operations: not the harness, not the installer, and not one of the VPS's three jobs. It stays here until it is given one, and this line is the record that it is unplaced. |
| Release **serving** — `internal/updater/manager.go` asks `https://api.github.com/repos/acme/agent_b/releases/latest` | **Moves to the VPS when the VPS exists**, under job 3. One constant. The release *notes* and the release *build* stay here. |
| `internal/telemetry` | Does not exist yet. Under 2jg only the **sender** is built here; the receiver is the VPS's. |
| Phone connection UI | **Lives in the phone client repository.** This Windows harness exposes only broker pairing in Settings and the versioned app-message contract; it no longer ships a browser phone client. |
| `internal/projection/testdata/tapes/`, `tools/projection-tape/` | **Stays, and is published.** This is *What crosses a boundary* working as intended, not something awaiting a move. |
| Anything Apple or Android | **None exists here.** |

## Deferred

- **Whether the git repository is renamed.** The deliverable is `windows` and it
  is this repository, with its history. Whether the remote `acme/agent_b` is
  renamed to match is operator account state, and is not a decision this map
  makes or needs.
- **The account model** is item 2kq's — the VPS's identity layer, which now also
  governs who a telemetry batch and a release request belong to.
