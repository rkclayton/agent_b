# Connectors

Connectors give `call_service` a named address, identity reference, and method allowlist. Add the credential in **Settings → Security → Credentials**, then ask Agent_b to add the connector and approve its connector card. Saved connectors are listed in **Settings → Connections → Connectors**.

An absolute HTTPS service URL uses the key or sign-in saved for its exact origin. Add a key in **Settings → Security → Credentials → Add → Key**, and enter each approved origin on its own line. Logins accept `user@host` or `user@host:port`, one per line. A named connector can still select its credential explicitly. An address can be listed by one credential. **Ask before use** applies to credentials selected by address as well as to named bindings.

## Gmail read-only

First add a Google credential for the exact origin `https://gmail.googleapis.com` with scope `https://www.googleapis.com/auth/gmail.readonly`. Then add this connector, replacing `<name>` with that credential's name:

```json
{
  "name": "gmail",
  "kind": "http",
  "base_url": "https://gmail.googleapis.com",
  "auth": "google:<name>",
  "allowed_methods": ["GET"]
}
```

The ledger skill uses only `messages.list` and `messages.get`. The `GET` allowlist makes every modifying Gmail endpoint unavailable to it.
