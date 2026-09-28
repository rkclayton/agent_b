# Talking to Agent_b from your phone

Item 2mx. This is the whole of it: a Shortcut that sends what you said to Agent_b, and a
second one that asks what came of it. Nothing here changes where Agent_b listens.

## What this does not do

Agent_b listens on `127.0.0.1` and nothing in it will ever bind anywhere else. Your phone
reaches it through **`tailscale serve`**, which is yours to run and yours to stop — the
product does not configure it, start it or depend on it. Nothing here approves a tool
call by voice either: an approval is something you read and answer on a screen.

## Once, on the machine Agent_b runs on

Tailscale publishes the loopback port to your tailnet and terminates TLS for it:

```
tailscale serve --bg 8790
tailscale serve status
```

The status line gives you the address to use below. It looks like
`https://<machine>.<tailnet>.ts.net/`. If `tailscale serve` is not available to you, the
alternative is a reverse proxy **you** run with TLS of your own — not a change to where
Agent_b binds.

## Once, in Agent_b

Settings → Security → Phone access → **Pair a phone**. It shows a six-digit code, good
for five minutes and usable once. Redeem it from the phone:

```
curl -s -X POST https://<machine>.<tailnet>.ts.net/api/phone/enrolment/redeem \
  -H 'content-type: application/json' \
  -d '{"code":"123456","name":"my phone"}'
```

The answer carries a **bearer token**. That token is the only credential a Shortcut needs
— it authenticates reads and writes both, so there is nothing else to carry. Keep it in
the Shortcut and nowhere else; anyone holding it has the same authority over Agent_b that
you do at the keyboard. Revoke it from the same page.

## Shortcut one: say something to Agent_b

1. **Dictate Text** — this is what you said.
2. **Text** action, set to a fresh UUID: add the *Get Contents of URL* step below and
   use Shortcuts' `UUID` variable. This is the idempotency key, and it is what makes a
   retry on a bad connection safe.
3. **Get Contents of URL**
   - URL: `https://<machine>.<tailnet>.ts.net/api/message`
   - Method: `POST`
   - Headers:
     - `Authorization`: `Bearer <token>`
     - `Content-Type`: `application/json`
     - `Idempotency-Key`: the UUID from step 2
   - Request Body (JSON): `{ "text": "<Dictated Text>" }`
4. **Get Dictionary Value** — `run_id` from the result. Keep it: the second Shortcut
   needs it.
5. **Speak Text** — "Sent."

There is no `session_id` in that body on purpose. A spoken request names no chat, so
Agent_b puts it in one called **Siri**, which it creates the first time and reuses after
that. You will see it in your chat list like any other. To send to a different chat
instead, add `"session_id"` to the body.

**Why the key matters.** Send the same `Idempotency-Key` twice and you get the same
answer back and **no second run**. Without it, a retry starts another run, and two runs
acting on one instruction is the outcome worth avoiding.

## Shortcut two: ask what came of it

1. **Get Contents of URL**
   - URL: `https://<machine>.<tailnet>.ts.net/api/runs/<run_id>/brief`
   - Method: `GET`
   - Header: `Authorization`: `Bearer <token>`
2. **Get Dictionary Value** — `spoken`.
3. **Speak Text** — that value.

`spoken` is one line, and every word of it comes from something that already happened:

| What is true | What you hear |
| --- | --- |
| A tool call is waiting for you | `Agent_b needs your approval to <what it wants to do>` |
| The model answered | the answer itself |
| The run stopped for a reason | `The run stopped because of <the reason, in words>.` |
| It is still going | `Still working.` |
| Nothing has happened yet | `Nothing to report yet.` |

Nothing is summarised, embellished or invented. A long answer is cut on a word boundary
and says so, and the markdown it was written in is stripped, because a listener hears
asterisks as nothing at all.

The same reply also carries `state` — one of `queued`, `running`, `needs_approval`,
`done`, `failed`, `stopped` — so a Shortcut can branch without reading the sentence, plus
`pending_approval`, `last_reply`, `last_stop_reason`, `status` and `chat_url`, which is
where to go to answer an approval.

## When it does not work

**The URL does nothing and Shortcuts reports it could not connect.** `tailscale serve` is
not running, or Agent_b is not. Check `tailscale serve status` on the machine, then that
Agent_b is up. Nothing on the phone can fix either.

**It answers 401.** The bearer token is wrong, or it was revoked. Pair the phone again
from Settings → Security → Phone access and replace the token in both Shortcuts. A code
is single-use and expires after five minutes, so an unused one from yesterday will not
work.

**You hear `Agent_b needs your approval to …` and nothing else ever happens.** That is
correct and it is waiting for you. Approvals are answered on a screen, by design — open
the chat and decide. Asking for the brief again will keep saying the same thing until
you do.
