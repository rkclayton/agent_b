# Google credentials

Agent_b can sign a connector in to a Google API. Sign-in stays in **Settings → Security → Credentials**; tools can use an existing sign-in but cannot start one.

## Configure Google Cloud

1. In Google Cloud Console, configure the OAuth consent screen as **External**, leave it in **Testing**, add your Google account as a test user, and enable the API the connector will call.
2. Open **Credentials**, choose **Create credentials → OAuth client ID → Desktop app**, then copy its client ID and client secret. Agent_b uses an available loopback redirect with authorization-code PKCE.
3. Add a Google credential in Agent_b with its name, exact Google API origin, client ID, client secret, and space-separated scopes; then press **Sign in with browser**.

Agent_b needs only those OAuth values. The client secret and refresh token are protected with Windows user-scope DPAPI. Access tokens remain only in process memory.

## Connector reference

Use `google:<credential-name>` as the connector auth value. The connector address must have the same HTTPS scheme, host, and port as the stored origin. For Gmail, use `https://gmail.googleapis.com`.

The complete read-only Gmail connector is in [CONNECTORS.md](CONNECTORS.md#gmail-read-only). Add the Google credential in **Settings → Security → Credentials**, then approve the connector once; it appears under **Settings → Connections → Connectors**.

**Switch account** replaces the stored account. **Sign out** asks Google to revoke the refresh token and then clears the protected local sign-in. Google does not offer device-code sign-in here.
