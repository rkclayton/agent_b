# The pairing link — `agentb-pairing-link-v1`

Version **1**. This is the one definition. Every repository that shows or reads a pairing link —
the Windows harness that shows it, the iOS app that scans it — implements exactly this and nothing
more. Where an implementation and this document disagree, this document is wrong or the
implementation is; they do not both get to be right.

## The link

```
https://agentb.app/pair#c=<code>&k=<key>
```

- `<code>` is the broker's pairing code **exactly as the broker issued it**, hyphens and all. This
  document does not shorten, re-encode or re-case it. Its length is its margin, and changing it
  would be a wire change in three repositories.
- `<key>` is the Ed25519 **identity public key of the Agent_b that is offering the pairing**: 32
  bytes, base64url, **no padding**.

Both values are in the **fragment**. A fragment is never sent to a server, so opening the link in a
browser — which is what happens when a phone with no app scans it — reaches `agentb.app` with the
path only and nothing else. That is the whole reason the fragment is used.

## Reading one

A reader:

1. Accepts the link only when the scheme is `https`, the host is `agentb.app` and the path is
   `/pair`.
2. Parses the fragment as `&`-separated `name=value` pairs.
3. Requires `c` and `k`. **Ignores every other field**, including ones added later — an unknown
   field is not an error, which is what makes this version number able to stay 1.
4. Decodes `k` as unpadded base64url and requires exactly 32 bytes.
5. Uses `c` as the broker pairing code, unchanged.

A reader that cannot satisfy 1, 3 or 4 refuses the whole link. It does not use part of one.

## What the key is for

The broker sends each endpoint the other's public keys in `PAIR_PEER`. A phone that scanned this
link already holds the Agent_b's Ed25519 identity key, so it can compare the key the broker sent it
with the key it scanned, itself, before confirming. The operator's own comparison of the
ten-group fingerprint on the two screens stays available and becomes a second check rather than
the only one.

## What must not happen to it

The link carries a live pairing code. It is shown and then it is gone:

- It is **never stored**: not in a file, not in a database, not in a preference.
- It is **never logged**: not in an event, not in History, not in a chat journal, not in telemetry,
  not in a diagnostic export.
- It exists in the page only while the code is live, and goes when the code expires, is used, or
  the pairing is cancelled.

A link that has been written down somewhere is a pairing code someone else can use.
