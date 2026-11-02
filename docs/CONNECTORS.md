# Connectors

Connectors give `call_service` a named address, identity reference, and method allowlist. Add the credential in **Settings → Security → Credentials**, then ask Agent_b to add the connector and approve its connector card. Saved connectors are listed in **Settings → Connections → Connectors**.

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
