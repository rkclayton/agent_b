# Talking to Agent_b from a phone

There is one phone connection path: pair the phone app through the broker.

Open **Settings → Security → Phone**, choose **Pair a phone**, scan the QR code in the
phone app, compare the fingerprint on both screens, and confirm it. The pairing survives
an Agent_b restart. **Revoke** removes that phone without changing the desktop identity.

Agent_b continues to listen only on `127.0.0.1`. The broker carries ciphertext and cannot
read the phone's messages. The retired browser page, six-digit enrolment code, bearer
token, Tailscale/Shortcut setup, and Web Push subscription are not connection options.

If the phone does not connect, read the connection line and pairing log in the same
**Phone** section. A broker-unreachable state names the transport failure; a fingerprint
mismatch means cancel the pairing and begin again.
